package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/LeezyWannaFall/Lunara/internal/storage"
)

type watchSource struct {
	calls  []time.Time
	failAt int
}

func (s *watchSource) FetchWeek(_ context.Context, group int64, monday time.Time) (schedule.Schedule, error) {
	s.calls = append(s.calls, monday)
	if s.failAt > 0 && len(s.calls) == s.failAt {
		return schedule.Schedule{}, errors.New("source")
	}
	return schedule.Schedule{GroupID: group, Monday: monday, WeekType: schedule.Upper, Status: schedule.Unpublished, Lessons: []schedule.Lesson{}, CheckedAt: time.Now(), NormalizationVersion: 1, Hash: "hash"}, nil
}

type watchRepo struct {
	saved, observed int
	reset           []time.Time
	result          storage.ObservationResult
}

func (r *watchRepo) SaveInitialWeeks(context.Context, []schedule.Schedule) (int, error) {
	r.saved++
	return 0, nil
}
func (r *watchRepo) ObserveBatchWithOptions(_ context.Context, weeks []schedule.Schedule, _ time.Time, _ storage.ObserveOptions) (storage.ObservationResult, error) {
	r.observed = len(weeks)
	return r.result, nil
}
func (r *watchRepo) ResetCandidates(_ context.Context, _ int64, weeks []time.Time) error {
	r.reset = append([]time.Time(nil), weeks...)
	return nil
}

func watcherCalendar(t *testing.T) schedule.Calendar {
	t.Helper()
	loc, _ := time.LoadLocation("Europe/Moscow")
	anchor, _ := schedule.ParseDate("2025-09-01", loc)
	return schedule.Calendar{Location: loc, AnchorMonday: anchor, AnchorType: schedule.Upper}
}

func TestWatcherCycleFetchesCompleteBatch(t *testing.T) {
	source, repo := &watchSource{}, &watchRepo{result: storage.ObservationResult{Status: storage.ObservationUnchanged}}
	w, err := NewWatcher(source, repo, watcherCalendar(t), 1306, 3, time.Hour, 10*time.Minute, 30*time.Minute, -100, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	w.now = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, watcherCalendar(t).Location) }
	result, err := w.RunCycle(context.Background())
	if err != nil || result.Status != storage.ObservationUnchanged || len(source.calls) != 3 || repo.saved != 1 || repo.observed != 3 {
		t.Fatalf("result=%+v calls=%d repo=%+v err=%v", result, len(source.calls), repo, err)
	}
	if !source.calls[0].Equal(schedule.Monday(w.now(), watcherCalendar(t).Location)) {
		t.Fatal("watch did not begin with current Monday")
	}
}

func TestWatcherSourceFailureResetsWholeRange(t *testing.T) {
	source, repo := &watchSource{failAt: 2}, &watchRepo{}
	w, _ := NewWatcher(source, repo, watcherCalendar(t), 1306, 3, time.Hour, 10*time.Minute, 30*time.Minute, 0, nil)
	w.now = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, watcherCalendar(t).Location) }
	if _, err := w.RunCycle(context.Background()); err == nil {
		t.Fatal("source error hidden")
	}
	if len(repo.reset) != 3 || repo.saved != 0 || repo.observed != 0 {
		t.Fatalf("repo=%+v", repo)
	}
}
