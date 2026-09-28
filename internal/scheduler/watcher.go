package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/LeezyWannaFall/Lunara/internal/storage"
)

type WatchRepository interface {
	SaveInitialWeeks(context.Context, []schedule.Schedule) (int, error)
	ObserveBatchWithOptions(context.Context, []schedule.Schedule, time.Time, storage.ObserveOptions) (storage.ObservationResult, error)
	ResetCandidates(context.Context, int64, []time.Time) error
}

type Watcher struct {
	source            Source
	repo              WatchRepository
	calendar          schedule.Calendar
	groupID           int64
	weeks             int
	interval          time.Duration
	confirmationDelay time.Duration
	chatID            int64
	logger            *slog.Logger
	now               func() time.Time
}

func NewWatcher(source Source, repo WatchRepository, calendar schedule.Calendar, groupID int64, weeks int, interval, confirmationDelay time.Duration, chatID int64, logger *slog.Logger) (*Watcher, error) {
	if source == nil || repo == nil || groupID <= 0 || weeks < 1 || weeks > 12 || interval <= 0 || confirmationDelay < storage.ConfirmationDelay {
		return nil, fmt.Errorf("invalid watcher configuration")
	}
	if err := calendar.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Watcher{source: source, repo: repo, calendar: calendar, groupID: groupID, weeks: weeks, interval: interval, confirmationDelay: confirmationDelay, chatID: chatID, logger: logger, now: time.Now}, nil
}

func (w *Watcher) RunCycle(ctx context.Context) (storage.ObservationResult, error) {
	now := w.now()
	first := schedule.Monday(now, w.calendar.Location)
	mondays := make([]time.Time, w.weeks)
	weeks := make([]schedule.Schedule, 0, w.weeks)
	for i := range mondays {
		mondays[i] = first.AddDate(0, 0, 7*i)
	}
	for i := range mondays {
		week, err := w.source.FetchWeek(ctx, w.groupID, mondays[i])
		if err != nil {
			resetErr := w.repo.ResetCandidates(ctx, w.groupID, mondays)
			if resetErr != nil {
				return storage.ObservationResult{}, fmt.Errorf("source: %v; reset candidates: %w", err, resetErr)
			}
			return storage.ObservationResult{}, &SourceError{Monday: mondays[i], Err: err}
		}
		if week.GroupID != w.groupID || !week.Monday.Equal(mondays[i]) {
			_ = w.repo.ResetCandidates(ctx, w.groupID, mondays)
			return storage.ObservationResult{}, &SourceError{Monday: mondays[i], Err: fmt.Errorf("source returned a different group or week")}
		}
		weeks = append(weeks, week)
	}
	if _, err := w.repo.SaveInitialWeeks(ctx, weeks); err != nil {
		return storage.ObservationResult{}, fmt.Errorf("ensure watcher baselines: %w", err)
	}
	return w.repo.ObserveBatchWithOptions(ctx, weeks, now, storage.ObserveOptions{ConfirmationDelay: w.confirmationDelay, DeliveryChatID: w.chatID})
}

func (w *Watcher) Run(ctx context.Context) {
	delay := time.Duration(0)
	for {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}
		result, err := w.RunCycle(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			w.logger.Warn("schedule watcher cycle failed", "error", err)
			delay = w.interval
			continue
		}
		w.logger.Info("schedule watcher cycle completed", "status", result.Status, "changed_weeks", len(result.Weeks))
		if result.Status == storage.ObservationPending {
			delay = w.confirmationDelay
		} else {
			delay = w.interval
		}
	}
}
