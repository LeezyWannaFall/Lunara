// Package scheduler coordinates initial schedule loading and, later, periodic checks.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/LeezyWannaFall/Lunara/internal/storage"
)

// These two interfaces cover external I/O only.
type Source interface {
	FetchWeek(context.Context, int64, time.Time) (schedule.Schedule, error)
}
type Repository interface {
	GetWeek(context.Context, int64, time.Time) (schedule.Schedule, error)
	SaveInitialWeeks(context.Context, []schedule.Schedule) (int, error)
}

type BootstrapResult struct{ Cached, Inserted int }

type SourceError struct {
	Monday time.Time
	Err    error
}

func (e *SourceError) Error() string {
	return fmt.Sprintf("load source week %s: %v", e.Monday.Format(time.DateOnly), e.Err)
}
func (e *SourceError) Unwrap() error { return e.Err }

// Bootstrap loads only missing weeks, starting with the week containing now.
// Source failures leave the entire missing batch unsaved; cached weeks survive.
// No notifications or periodic work are performed at this stage.
func Bootstrap(ctx context.Context, source Source, repo Repository, calendar schedule.Calendar, groupID int64, now time.Time, count int) (BootstrapResult, error) {
	var report BootstrapResult
	if err := calendar.Validate(); err != nil {
		return report, err
	}
	if groupID <= 0 || now.IsZero() || count < 1 || count > 12 {
		return report, fmt.Errorf("invalid bootstrap group, date or week count")
	}
	first := schedule.Monday(now, calendar.Location)
	missing := []time.Time{}
	for i := 0; i < count; i++ {
		monday := first.AddDate(0, 0, 7*i)
		_, err := repo.GetWeek(ctx, groupID, monday)
		switch {
		case err == nil:
			report.Cached++
		case errors.Is(err, storage.ErrNotFound):
			missing = append(missing, monday)
		default:
			return report, fmt.Errorf("read bootstrap cache: %w", err)
		}
	}
	if len(missing) == 0 {
		return report, nil
	}
	weeks := make([]schedule.Schedule, 0, len(missing))
	for _, monday := range missing {
		week, err := source.FetchWeek(ctx, groupID, monday)
		if err != nil {
			return report, &SourceError{Monday: monday, Err: err}
		}
		if week.GroupID != groupID || !week.Monday.Equal(monday) {
			return report, &SourceError{Monday: monday, Err: fmt.Errorf("source returned a different group or week")}
		}
		weeks = append(weeks, week)
	}
	inserted, err := repo.SaveInitialWeeks(ctx, weeks)
	if err != nil {
		return report, fmt.Errorf("persist bootstrap batch: %w", err)
	}
	report.Inserted = inserted
	return report, nil
}
