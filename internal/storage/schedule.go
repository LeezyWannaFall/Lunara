// Package storage stores normalized weekly schedules in PostgreSQL.
package storage

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("schedule week not stored")

const telegramOffsetKey = "telegram_update_offset"

type Store struct {
	pool     *pgxpool.Pool
	calendar schedule.Calendar
}

func New(pool *pgxpool.Pool, calendar schedule.Calendar) (*Store, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL pool is required")
	}
	if err := calendar.Validate(); err != nil {
		return nil, err
	}
	return &Store{pool: pool, calendar: calendar}, nil
}

func (s *Store) Close() { s.pool.Close() }

// TelegramOffset is the first update ID that has not been processed yet.
func (s *Store) TelegramOffset(ctx context.Context) (int64, error) {
	var offset int64
	err := s.pool.QueryRow(ctx, `SELECT value FROM bot_state WHERE key=$1`, telegramOffsetKey).Scan(&offset)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read Telegram offset: %w", err)
	}
	return offset, nil
}

// SaveTelegramOffset only moves the cursor forward, making repeated saves safe.
func (s *Store) SaveTelegramOffset(ctx context.Context, offset int64) error {
	if offset < 0 {
		return fmt.Errorf("Telegram offset must not be negative")
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO bot_state (key,value) VALUES ($1,$2)
 ON CONFLICT (key) DO UPDATE SET value=GREATEST(bot_state.value,EXCLUDED.value), updated_at=now()`, telegramOffsetKey, offset)
	if err != nil {
		return fmt.Errorf("save Telegram offset: %w", err)
	}
	return nil
}

// GetWeek accepts any date in a week. A missing snapshot differs from an
// explicitly unpublished week. Read failures never return partial schedules.
func (s *Store) GetWeek(ctx context.Context, groupID int64, date time.Time) (schedule.Schedule, error) {
	if groupID <= 0 || date.IsZero() {
		return schedule.Schedule{}, fmt.Errorf("positive group ID and date are required")
	}
	monday := schedule.Monday(date, s.calendar.Location)
	var result schedule.Schedule
	var lessons []byte
	var weekType, status string
	err := s.pool.QueryRow(ctx, `
 SELECT s.group_id, s.week_type, s.status, s.lessons,
        s.normalization_version, s.content_hash, s.checked_at
 FROM schedule_heads h JOIN schedule_snapshots s ON s.id = h.snapshot_id
 WHERE h.group_id = $1 AND h.week_start = $2::date`, groupID, monday.Format(time.DateOnly)).Scan(
		&result.GroupID, &weekType, &status, &lessons,
		&result.NormalizationVersion, &result.Hash, &result.CheckedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return schedule.Schedule{}, ErrNotFound
	}
	if err != nil {
		return schedule.Schedule{}, fmt.Errorf("read schedule week: %w", err)
	}
	result.CheckedAt = result.CheckedAt.UTC()
	result.Monday, result.WeekType, result.Status = monday, schedule.WeekType(weekType), schedule.Status(status)
	if err := json.Unmarshal(lessons, &result.Lessons); err != nil {
		return schedule.Schedule{}, fmt.Errorf("decode stored lessons: %w", err)
	}
	for i := range result.Lessons {
		result.Lessons[i].Date = schedule.LocalDate(result.Lessons[i].Date, s.calendar.Location)
	}
	return result, nil
}

// SaveInitialWeeks atomically fills missing weeks for one group. Existing heads
// are never replaced, even by a different or unpublished source response.
// Group-scoped transaction locking makes concurrent bootstrap calls idempotent.
func (s *Store) SaveInitialWeeks(ctx context.Context, weeks []schedule.Schedule) (int, error) {
	if len(weeks) == 0 {
		return 0, nil
	}
	ordered := append([]schedule.Schedule(nil), weeks...)
	seen := map[string]bool{}
	for _, week := range ordered {
		if week.GroupID != ordered[0].GroupID {
			return 0, fmt.Errorf("bootstrap batch must contain one group")
		}
		if err := s.validate(week); err != nil {
			return 0, err
		}
		key := week.Monday.In(s.calendar.Location).Format(time.DateOnly)
		if seen[key] {
			return 0, fmt.Errorf("duplicate week in bootstrap batch")
		}
		seen[key] = true
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Monday.Before(ordered[j].Monday) })
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin initial schedule save: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	lockKey := "lunara:bootstrap:" + strconv.FormatInt(ordered[0].GroupID, 10)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return 0, fmt.Errorf("lock bootstrap batch: %w", err)
	}
	inserted := 0
	for _, week := range ordered {
		monday := week.Monday.In(s.calendar.Location).Format(time.DateOnly)
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schedule_heads WHERE group_id=$1 AND week_start=$2::date)`, week.GroupID, monday).Scan(&exists); err != nil {
			return 0, fmt.Errorf("check existing schedule: %w", err)
		}
		if exists {
			continue
		}
		lessons, err := json.Marshal(week.Lessons)
		if err != nil {
			return 0, fmt.Errorf("encode lessons: %w", err)
		}
		var id int64
		err = tx.QueryRow(ctx, `INSERT INTO schedule_snapshots
   (group_id,week_start,week_type,status,lessons,normalization_version,content_hash,checked_at)
   VALUES ($1,$2::date,$3,$4,$5::jsonb,$6,$7,$8) RETURNING id`,
			week.GroupID, monday, string(week.WeekType), string(week.Status), lessons, week.NormalizationVersion, week.Hash, week.CheckedAt).Scan(&id)
		if err != nil {
			return 0, fmt.Errorf("save initial snapshot: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schedule_heads (group_id,week_start,snapshot_id) VALUES ($1,$2::date,$3)`, week.GroupID, monday, id); err != nil {
			return 0, fmt.Errorf("save initial head: %w", err)
		}
		inserted++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit initial schedules: %w", err)
	}
	return inserted, nil
}

func (s *Store) validate(week schedule.Schedule) error {
	if week.GroupID <= 0 || week.Monday.IsZero() || !week.Monday.Equal(schedule.Monday(week.Monday, s.calendar.Location)) {
		return fmt.Errorf("invalid schedule group or week start")
	}
	expected, err := s.calendar.WeekTypeAt(week.Monday)
	if err != nil || expected != week.WeekType {
		return fmt.Errorf("schedule week type disagrees with calendar")
	}
	hash, err := hex.DecodeString(week.Hash)
	if err != nil || len(hash) != 32 || strings.ToLower(week.Hash) != week.Hash || week.NormalizationVersion <= 0 || week.CheckedAt.IsZero() {
		return fmt.Errorf("invalid snapshot metadata")
	}
	if week.Lessons == nil || week.Status == schedule.Published && len(week.Lessons) == 0 || week.Status == schedule.Unpublished && len(week.Lessons) != 0 || week.Status != schedule.Published && week.Status != schedule.Unpublished {
		return fmt.Errorf("invalid schedule publication status")
	}
	start := schedule.Monday(week.Monday, s.calendar.Location)
	for _, lesson := range week.Lessons {
		if lesson.Date.IsZero() || !lesson.Date.Equal(schedule.LocalDate(lesson.Date, s.calendar.Location)) || lesson.Date.Before(start) || !lesson.Date.Before(start.AddDate(0, 0, 7)) {
			return fmt.Errorf("lesson date outside schedule week")
		}
		if lesson.StartMinute < 0 || lesson.EndMinute >= 24*60 || lesson.EndMinute <= lesson.StartMinute || strings.TrimSpace(lesson.Subject) == "" {
			return fmt.Errorf("invalid lesson time or subject")
		}
	}
	return nil
}
