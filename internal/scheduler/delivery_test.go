package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/changes"
	"github.com/LeezyWannaFall/Lunara/internal/storage"
	"github.com/LeezyWannaFall/Lunara/internal/telegram"
)

type deliveryRepo struct {
	item      storage.Delivery
	claimErr  error
	completed bool
	retryAt   time.Time
	permanent bool
}

func (r *deliveryRepo) ClaimDelivery(context.Context, time.Time, time.Duration) (storage.Delivery, error) {
	return r.item, r.claimErr
}
func (r *deliveryRepo) CompleteDelivery(context.Context, int64, int64, time.Time) error {
	r.completed = true
	return nil
}
func (r *deliveryRepo) RetryDelivery(_ context.Context, _ int64, next time.Time, _ string, permanent bool) error {
	r.retryAt, r.permanent = next, permanent
	return nil
}

type deliverySender struct{ err error }

func (s deliverySender) SendNotification(context.Context, int64, string) (int64, error) {
	return 99, s.err
}

func testDelivery() storage.Delivery {
	return storage.Delivery{ID: 1, ChatID: -100, Attempts: 1, ChangeSet: changes.ChangeSet{ID: 2, DetectedAt: time.Now(), Kind: changes.FirstPublication, WeekStarts: []time.Time{time.Now()}}}
}

func TestDeliverySuccessAndNoWork(t *testing.T) {
	repo := &deliveryRepo{item: testDelivery()}
	w, _ := NewDeliveryWorker(repo, deliverySender{}, time.UTC, time.Second, nil)
	if ok, err := w.ProcessOne(context.Background()); !ok || err != nil || !repo.completed {
		t.Fatalf("ok=%v err=%v repo=%+v", ok, err, repo)
	}
	repo.claimErr = storage.ErrNoDelivery
	if ok, err := w.ProcessOne(context.Background()); ok || err != nil {
		t.Fatalf("empty ok=%v err=%v", ok, err)
	}
}

func TestDeliveryUsesRetryAfterAndPermanentFailure(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	repo := &deliveryRepo{item: testDelivery()}
	w, _ := NewDeliveryWorker(repo, deliverySender{err: &telegram.APIError{Code: 429, Description: "slow", RetryAfter: 17 * time.Second}}, time.UTC, time.Second, nil)
	w.now = func() time.Time { return now }
	if ok, err := w.ProcessOne(context.Background()); !ok || err == nil || !repo.retryAt.Equal(now.Add(17*time.Second)) || repo.permanent {
		t.Fatalf("retry=%v permanent=%v err=%v", repo.retryAt, repo.permanent, err)
	}
	repo = &deliveryRepo{item: testDelivery()}
	w.repo, w.sender = repo, deliverySender{err: &telegram.APIError{Code: 400, Description: "bad chat"}}
	if _, err := w.ProcessOne(context.Background()); err == nil || !repo.permanent {
		t.Fatalf("permanent=%v err=%v", repo.permanent, err)
	}
}
