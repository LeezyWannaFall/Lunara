package scheduler

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/LeezyWannaFall/Lunara/internal/storage"
)

type fakeSource struct {
	calls    []time.Time
	weeks    map[string]schedule.Schedule
	failDate string
}

func (f *fakeSource) FetchWeek(ctx context.Context, group int64, date time.Time) (schedule.Schedule, error) {
	if err := ctx.Err(); err != nil {
		return schedule.Schedule{}, err
	}
	f.calls = append(f.calls, date)
	if date.Format(time.DateOnly) == f.failDate {
		return schedule.Schedule{}, fmt.Errorf("source unavailable")
	}
	return f.weeks[date.Format(time.DateOnly)], nil
}

type fakeRepository struct {
	weeks                map[string]schedule.Schedule
	saveCalls            int
	readError, saveError error
}

func (r *fakeRepository) GetWeek(ctx context.Context, group int64, date time.Time) (schedule.Schedule, error) {
	if err := ctx.Err(); err != nil {
		return schedule.Schedule{}, err
	}
	if r.readError != nil {
		return schedule.Schedule{}, r.readError
	}
	week, ok := r.weeks[date.Format(time.DateOnly)]
	if !ok {
		return schedule.Schedule{}, storage.ErrNotFound
	}
	return week, nil
}
func (r *fakeRepository) SaveInitialWeeks(ctx context.Context, weeks []schedule.Schedule) (int, error) {
	r.saveCalls++
	if r.saveError != nil {
		return 0, r.saveError
	}
	for _, week := range weeks {
		r.weeks[week.Monday.Format(time.DateOnly)] = week
	}
	return len(weeks), nil
}
func testData(t *testing.T) (schedule.Calendar, map[string]schedule.Schedule) {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := schedule.ParseDate("2025-09-01", loc)
	if err != nil {
		t.Fatal(err)
	}
	calendar := schedule.Calendar{Location: loc, AnchorMonday: anchor, AnchorType: schedule.Upper}
	weeks := map[string]schedule.Schedule{}
	for _, text := range []string{"2026-09-28", "2026-10-05"} {
		date, err := schedule.ParseDate(text, loc)
		if err != nil {
			t.Fatal(err)
		}
		weeks[text] = schedule.Schedule{GroupID: 1306, Monday: date, Status: schedule.Published}
	}
	return calendar, weeks
}
func TestBootstrapOnlyMissingWeeks(t *testing.T) {
	calendar, weeks := testData(t)
	source := &fakeSource{weeks: weeks}
	repo := &fakeRepository{weeks: map[string]schedule.Schedule{}}
	now := weeks["2026-09-28"].Monday.AddDate(0, 0, 6)
	report, err := Bootstrap(context.Background(), source, repo, calendar, 1306, now, 2)
	if err != nil || report.Cached != 0 || report.Inserted != 2 || repo.saveCalls != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	source.calls = nil
	report, err = Bootstrap(context.Background(), source, repo, calendar, 1306, now, 2)
	if err != nil || report.Cached != 2 || report.Inserted != 0 || len(source.calls) != 0 || repo.saveCalls != 1 {
		t.Fatalf("restart contacted source or saved again: %+v %v", report, err)
	}
	delete(repo.weeks, "2026-10-05")
	report, err = Bootstrap(context.Background(), source, repo, calendar, 1306, now, 2)
	if err != nil || report.Cached != 1 || report.Inserted != 1 || len(source.calls) != 1 {
		t.Fatalf("missing week: %+v %v", report, err)
	}
}
func TestBootstrapFailureLeavesBatchUnsaved(t *testing.T) {
	calendar, weeks := testData(t)
	source := &fakeSource{weeks: weeks, failDate: "2026-10-05"}
	repo := &fakeRepository{weeks: map[string]schedule.Schedule{}}
	_, err := Bootstrap(context.Background(), source, repo, calendar, 1306, weeks["2026-09-28"].Monday, 2)
	var sourceError *SourceError
	if !errors.As(err, &sourceError) || repo.saveCalls != 0 || len(repo.weeks) != 0 {
		t.Fatalf("partially saved failed batch: %v", err)
	}
	repo.weeks["2026-09-28"] = weeks["2026-09-28"]
	report, err := Bootstrap(context.Background(), source, repo, calendar, 1306, weeks["2026-09-28"].Monday, 2)
	if !errors.As(err, &sourceError) || report.Cached != 1 || len(repo.weeks) != 1 {
		t.Fatalf("lost cache: %+v %v", report, err)
	}
}
func TestBootstrapDBErrorsAndCancellation(t *testing.T) {
	calendar, weeks := testData(t)
	dbErr := errors.New("database unavailable")
	for _, readFail := range []bool{true, false} {
		source := &fakeSource{weeks: weeks}
		repo := &fakeRepository{weeks: map[string]schedule.Schedule{}}
		if readFail {
			repo.readError = dbErr
		} else {
			repo.saveError = dbErr
		}
		_, err := Bootstrap(context.Background(), source, repo, calendar, 1306, weeks["2026-09-28"].Monday, 2)
		if !errors.Is(err, dbErr) {
			t.Fatalf("database failure hidden: %v", err)
		}
		if readFail && len(source.calls) != 0 {
			t.Fatal("DB error treated as cache miss")
		}
	}
	source := &fakeSource{weeks: weeks}
	repo := &fakeRepository{weeks: map[string]schedule.Schedule{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Bootstrap(ctx, source, repo, calendar, 1306, weeks["2026-09-28"].Monday, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if len(source.calls) != 0 || repo.saveCalls != 0 {
		t.Fatal("canceled bootstrap did I/O")
	}
}
func TestBootstrapRejectsMismatchedSourceAndWindow(t *testing.T) {
	calendar, weeks := testData(t)
	first := weeks["2026-09-28"].Monday
	bad := weeks["2026-09-28"]
	bad.GroupID = 999
	weeks["2026-09-28"] = bad
	source := &fakeSource{weeks: weeks}
	repo := &fakeRepository{weeks: map[string]schedule.Schedule{}}
	if _, err := Bootstrap(context.Background(), source, repo, calendar, 1306, first, 2); err == nil || repo.saveCalls != 0 {
		t.Fatal("saved mismatched source")
	}
	for _, count := range []int{0, 13} {
		if _, err := Bootstrap(context.Background(), source, repo, calendar, 1306, first, count); err == nil {
			t.Fatal("invalid count accepted")
		}
	}
}
