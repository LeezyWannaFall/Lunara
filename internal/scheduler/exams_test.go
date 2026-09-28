package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/LeezyWannaFall/Lunara/internal/storage"
)

type fakeExamSource struct {
	value schedule.ExamSchedule
	err   error
}

func (f fakeExamSource) FetchExams(context.Context, int64) (schedule.ExamSchedule, error) {
	return f.value, f.err
}

type fakeExamRepo struct{ saved int }

func (f *fakeExamRepo) SaveExams(context.Context, schedule.ExamSchedule) error { f.saved++; return nil }
func TestExamWatcherRetainsCacheOnSourceError(t *testing.T) {
	repo := &fakeExamRepo{}
	watcher, err := NewExamWatcher(fakeExamSource{err: errors.New("offline")}, repo, 1306, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if watcher.Refresh(context.Background()) == nil {
		t.Fatal("expected source error")
	}
	if repo.saved != 0 {
		t.Fatal("source error overwrote cache")
	}
}

type fakeReminderRepo struct{ keys []string }

func (f *fakeReminderRepo) EnqueueReminder(_ context.Context, _ int64, kind string, period time.Time, _ string) (bool, error) {
	key := kind + ":" + period.Format(time.DateOnly)
	for _, v := range f.keys {
		if v == key {
			return false, nil
		}
	}
	f.keys = append(f.keys, key)
	return true, nil
}
func (*fakeReminderRepo) ClaimReminder(context.Context, time.Time, time.Duration) (storage.ReminderDelivery, error) {
	return storage.ReminderDelivery{}, storage.ErrNoDelivery
}
func (*fakeReminderRepo) CompleteReminder(context.Context, int64, int64, time.Time) error { return nil }
func (*fakeReminderRepo) RetryReminder(context.Context, int64, time.Time, string, bool) error {
	return nil
}

type fakeReminderFormatter struct{}

func (fakeReminderFormatter) ReminderTomorrow(context.Context, time.Time) ([]string, error) {
	return []string{"tomorrow"}, nil
}
func (fakeReminderFormatter) ReminderNextWeek(context.Context, time.Time) ([]string, error) {
	return []string{"week"}, nil
}
func TestReminderSchedulerDueAndDeduplicated(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Moscow")
	repo := &fakeReminderRepo{}
	worker, err := NewReminderScheduler(repo, fakeReminderFormatter{}, -100, loc, true, true, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker.now = func() time.Time { return time.Date(2026, 9, 27, 19, 0, 0, 0, loc) }
	if err := worker.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := worker.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.keys) != 2 {
		t.Fatalf("wanted two unique reminders, got %v", repo.keys)
	}
}
