package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
)

type ExamSource interface {
	FetchExams(context.Context, int64) (schedule.ExamSchedule, error)
}
type ExamRepository interface {
	SaveExams(context.Context, schedule.ExamSchedule) error
}
type ExamWatcher struct {
	source   ExamSource
	repo     ExamRepository
	groupID  int64
	interval time.Duration
	logger   *slog.Logger
}

func NewExamWatcher(source ExamSource, repo ExamRepository, groupID int64, interval time.Duration, logger *slog.Logger) (*ExamWatcher, error) {
	if source == nil || repo == nil || groupID <= 0 || interval <= 0 {
		return nil, fmt.Errorf("invalid exam watcher configuration")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ExamWatcher{source: source, repo: repo, groupID: groupID, interval: interval, logger: logger}, nil
}
func (w *ExamWatcher) Refresh(ctx context.Context) error {
	value, err := w.source.FetchExams(ctx, w.groupID)
	if err != nil {
		return err
	}
	return w.repo.SaveExams(ctx, value)
}
func (w *ExamWatcher) Run(ctx context.Context) {
	for {
		if err := w.Refresh(ctx); err != nil && ctx.Err() == nil {
			w.logger.Warn("exam refresh failed; cached exams retained", "error", err)
		}
		timer := time.NewTimer(w.interval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}
