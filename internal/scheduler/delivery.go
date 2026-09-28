package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/storage"
	"github.com/LeezyWannaFall/Lunara/internal/telegram"
)

type DeliveryRepository interface {
	ClaimDelivery(context.Context, time.Time, time.Duration) (storage.Delivery, error)
	CompleteDelivery(context.Context, int64, int64, time.Time) error
	RetryDelivery(context.Context, int64, time.Time, string, bool) error
}

type NotificationSender interface {
	SendNotification(context.Context, int64, string) (int64, error)
}

type DeliveryWorker struct {
	repo         DeliveryRepository
	sender       NotificationSender
	location     *time.Location
	pollInterval time.Duration
	logger       *slog.Logger
	now          func() time.Time
}

func NewDeliveryWorker(repo DeliveryRepository, sender NotificationSender, location *time.Location, pollInterval time.Duration, logger *slog.Logger) (*DeliveryWorker, error) {
	if repo == nil || sender == nil || location == nil || pollInterval <= 0 {
		return nil, fmt.Errorf("invalid delivery worker configuration")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &DeliveryWorker{repo: repo, sender: sender, location: location, pollInterval: pollInterval, logger: logger, now: time.Now}, nil
}

func (w *DeliveryWorker) ProcessOne(ctx context.Context) (bool, error) {
	now := w.now()
	delivery, err := w.repo.ClaimDelivery(ctx, now, time.Minute)
	if errors.Is(err, storage.ErrNoDelivery) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	text := telegram.NotificationText(delivery.ChangeSet, w.location)
	messageID, sendErr := w.sender.SendNotification(ctx, delivery.ChatID, text)
	if sendErr == nil {
		return true, w.repo.CompleteDelivery(ctx, delivery.ID, messageID, w.now())
	}
	delay, retry := telegram.RetryAfter(sendErr)
	if !retry && telegram.Temporary(sendErr) {
		shift := delivery.Attempts - 1
		if shift < 0 {
			shift = 0
		}
		if shift > 6 {
			shift = 6
		}
		delay = time.Minute * time.Duration(1<<shift)
		retry = true
	}
	if err := w.repo.RetryDelivery(ctx, delivery.ID, now.Add(delay), sendErr.Error(), !retry); err != nil {
		return true, err
	}
	return true, sendErr
}

func (w *DeliveryWorker) Run(ctx context.Context) {
	for {
		processed, err := w.ProcessOne(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			w.logger.Warn("notification delivery failed", "error", err)
		}
		if processed {
			continue
		}
		timer := time.NewTimer(w.pollInterval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}
