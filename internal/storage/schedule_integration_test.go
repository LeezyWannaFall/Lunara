package storage_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/LeezyWannaFall/Lunara/internal/scheduler"
	"github.com/LeezyWannaFall/Lunara/internal/source/miigaik"
	"github.com/LeezyWannaFall/Lunara/internal/storage"
	"github.com/LeezyWannaFall/Lunara/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func calendar(t *testing.T) schedule.Calendar {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := schedule.ParseDate("2025-09-01", loc)
	if err != nil {
		t.Fatal(err)
	}
	return schedule.Calendar{Location: loc, AnchorMonday: anchor, AnchorType: schedule.Upper}
}
func date(t *testing.T, text string) time.Time {
	t.Helper()
	d, err := schedule.ParseDate(text, calendar(t).Location)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func week(t *testing.T, name, text string) schedule.Schedule {
	t.Helper()
	body, err := os.ReadFile("../source/miigaik/testdata/" + name + ".html")
	if err != nil {
		t.Fatal(err)
	}
	w, err := miigaik.ParseWeek(strings.NewReader(string(body)), 1306, date(t, text), calendar(t))
	if err != nil {
		t.Fatal(err)
	}
	w.CheckedAt = time.Now().UTC().Truncate(time.Microsecond)
	return w
}
func database(t *testing.T) (*storage.Store, *pgxpool.Pool, *goose.Provider) {
	t.Helper()
	dsn := os.Getenv("LUNARA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LUNARA_TEST_DATABASE_URL or run scripts/test-integration.sh")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		t.Fatal(err)
	}
	schemaName := fmt.Sprintf("lunara_test_%d", time.Now().UnixNano())
	schemaSQL := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaSQL); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP SCHEMA "+schemaSQL+" CASCADE")
		admin.Close()
		if err != nil {
			t.Errorf("clean test schema: %v", err)
		}
	})
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName
	sqlDB := stdlib.OpenDB(*cfg.ConnConfig)
	t.Cleanup(func() { sqlDB.Close() })
	provider, err := migrations.New(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := storage.New(pool, calendar(t))
	if err != nil {
		t.Fatal(err)
	}
	return store, pool, provider
}
func count(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostgresMigrations(t *testing.T) {
	_, pool, provider := database(t)
	ctx := context.Background()
	if version, err := provider.GetDBVersion(ctx); err != nil || version != 1 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if result, err := provider.Up(ctx); err != nil || len(result) != 0 {
		t.Fatalf("second up: %v %v", result, err)
	}
	if _, err := provider.Down(ctx); err != nil {
		t.Fatal(err)
	}
	if version, err := provider.GetDBVersion(ctx); err != nil || version != 0 {
		t.Fatalf("down version=%d err=%v", version, err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if count(t, pool, "schedule_heads") != 0 {
		t.Fatal("new tables not empty")
	}
}
func TestPostgresRoundTripAndBaselinePreservation(t *testing.T) {
	store, pool, _ := database(t)
	ctx := context.Background()
	first := week(t, "upper", "2026-09-28")
	second := week(t, "lower", "2026-10-05")
	if _, err := store.GetWeek(ctx, 1306, first.Monday); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if n, err := store.SaveInitialWeeks(ctx, []schedule.Schedule{second, first}); err != nil || n != 2 {
		t.Fatalf("save=%d err=%v", n, err)
	}
	// Read with a different Store instance, as on process restart.
	reopened, err := storage.New(pool, calendar(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.GetWeek(ctx, 1306, first.Monday.AddDate(0, 0, 6))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Fatalf(`round trip changed schedule:
%+v
%+v`, got, first)
	}
	changed := first
	changed.Hash = strings.Repeat("a", 64)
	changed.Status = schedule.Unpublished
	changed.Lessons = []schedule.Lesson{}
	if n, err := reopened.SaveInitialWeeks(ctx, []schedule.Schedule{changed, second}); err != nil || n != 0 {
		t.Fatalf("repeat save=%d err=%v", n, err)
	}
	got, err = reopened.GetWeek(ctx, 1306, first.Monday)
	if err != nil || got.Hash != first.Hash || got.Status != schedule.Published {
		t.Fatalf("baseline overwritten: %+v %v", got, err)
	}
	if count(t, pool, "schedule_snapshots") != 2 || count(t, pool, "schedule_heads") != 2 {
		t.Fatal("duplicate snapshots on restart")
	}
	empty := week(t, "empty", "2027-08-02")
	if n, err := store.SaveInitialWeeks(ctx, []schedule.Schedule{empty}); err != nil || n != 1 {
		t.Fatalf("empty save=%d err=%v", n, err)
	}
	got, err = store.GetWeek(ctx, 1306, empty.Monday)
	if err != nil || got.Status != schedule.Unpublished || len(got.Lessons) != 0 {
		t.Fatalf("unpublished: %+v %v", got, err)
	}
}
func TestPostgresBatchRollback(t *testing.T) {
	store, pool, _ := database(t)
	ctx := context.Background()
	// Force the second SQL insert to fail after the first snapshot/head were written.
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_second_week() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.week_start = DATE '2026-10-05' THEN RAISE EXCEPTION 'test failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_second_week BEFORE INSERT ON schedule_snapshots FOR EACH ROW EXECUTE FUNCTION reject_second_week();`)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := store.SaveInitialWeeks(ctx, []schedule.Schedule{week(t, "upper", "2026-09-28"), week(t, "lower", "2026-10-05")}); err == nil || n != 0 {
		t.Fatalf("failed batch reported success: %d %v", n, err)
	}
	if count(t, pool, "schedule_snapshots") != 0 || count(t, pool, "schedule_heads") != 0 {
		t.Fatal("partial batch committed")
	}
}
func TestPostgresConcurrentBootstrap(t *testing.T) {
	store, pool, _ := database(t)
	weeks := []schedule.Schedule{week(t, "upper", "2026-09-28"), week(t, "lower", "2026-10-05")}
	var wg sync.WaitGroup
	results := make(chan int, 6)
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := store.SaveInitialWeeks(context.Background(), weeks)
			results <- n
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	total := 0
	for n := range results {
		total += n
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if total != 2 || count(t, pool, "schedule_snapshots") != 2 || count(t, pool, "schedule_heads") != 2 {
		t.Fatal("concurrent bootstrap created duplicates")
	}
}
func TestPostgresValidationAndConstraints(t *testing.T) {
	store, pool, _ := database(t)
	ctx := context.Background()
	first := week(t, "upper", "2026-09-28")
	bad := first
	bad.Hash = "invalid"
	if _, err := store.SaveInitialWeeks(ctx, []schedule.Schedule{bad}); err == nil {
		t.Fatal("invalid metadata saved")
	}
	if count(t, pool, "schedule_snapshots") != 0 {
		t.Fatal("invalid data reached storage")
	}
	if _, err := store.SaveInitialWeeks(ctx, []schedule.Schedule{first}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO schedule_heads (group_id,week_start,snapshot_id) SELECT 999, week_start,id FROM schedule_snapshots`); err == nil {
		t.Fatal("head references another group's snapshot")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.GetWeek(canceled, 1306, first.Monday); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
}

type fixtureSource struct {
	weeks map[string]schedule.Schedule
	calls int
	fail  string
}

func (f *fixtureSource) FetchWeek(ctx context.Context, group int64, date time.Time) (schedule.Schedule, error) {
	f.calls++
	key := date.Format(time.DateOnly)
	if key == f.fail {
		return schedule.Schedule{}, fmt.Errorf("source failure")
	}
	return f.weeks[key], nil
}
func TestPostgresBootstrapSourceFailureAndRestart(t *testing.T) {
	store, pool, _ := database(t)
	ctx := context.Background()
	first := week(t, "upper", "2026-09-28")
	second := week(t, "lower", "2026-10-05")
	source := &fixtureSource{weeks: map[string]schedule.Schedule{"2026-09-28": first, "2026-10-05": second}, fail: "2026-10-05"}
	if _, err := scheduler.Bootstrap(ctx, source, store, calendar(t), 1306, first.Monday, 2); err == nil {
		t.Fatal("failed source accepted")
	}
	if count(t, pool, "schedule_heads") != 0 {
		t.Fatal("source failure committed partial baseline")
	}
	source.fail = ""
	if report, err := scheduler.Bootstrap(ctx, source, store, calendar(t), 1306, first.Monday, 2); err != nil || report.Inserted != 2 {
		t.Fatalf("initial load: %+v %v", report, err)
	}
	source.calls = 0
	source.fail = "2026-09-28"
	if report, err := scheduler.Bootstrap(ctx, source, store, calendar(t), 1306, first.Monday, 2); err != nil || report.Cached != 2 || source.calls != 0 {
		t.Fatalf("restart: %+v %v", report, err)
	}
}
