package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/storage"
	"github.com/LeezyWannaFall/Lunara/internal/telegram"
)

type ReminderRepository interface {
	EnqueueReminder(context.Context, int64, string, time.Time, string) (bool, error)
	ClaimReminder(context.Context, time.Time, time.Duration) (storage.ReminderDelivery, error)
	CompleteReminder(context.Context, int64, int64, time.Time) error
	RetryReminder(context.Context, int64, time.Time, string, bool) error
}
type ReminderFormatter interface {
	ReminderTomorrow(context.Context, time.Time) ([]string, error)
	ReminderNextWeek(context.Context, time.Time) ([]string, error)
}
type ReminderScheduler struct {
	repo             ReminderRepository
	formatter        ReminderFormatter
	chatID           int64
	location         *time.Location
	tomorrow, weekly bool
	interval         time.Duration
	logger           *slog.Logger
	now              func() time.Time
}

func NewReminderScheduler(repo ReminderRepository, formatter ReminderFormatter, chatID int64, loc *time.Location, tomorrow, weekly bool, interval time.Duration, logger *slog.Logger) (*ReminderScheduler, error) {
	if repo == nil || formatter == nil || loc == nil || interval <= 0 {
		return nil, fmt.Errorf("invalid reminder scheduler configuration")
	}
	if (tomorrow || weekly) && chatID == 0 {
		return nil, fmt.Errorf("reminders require TELEGRAM_CHAT_ID")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ReminderScheduler{repo: repo, formatter: formatter, chatID: chatID, location: loc, tomorrow: tomorrow, weekly: weekly, interval: interval, logger: logger, now: time.Now}, nil
}
func (w *ReminderScheduler) Tick(ctx context.Context) error {
	now := w.now().In(w.location)
	if now.Hour() != 19 || now.Minute() != 0 {
		return nil
	}
	if w.tomorrow {
		messages, err := w.formatter.ReminderTomorrow(ctx, now)
		if err != nil {
			return err
		}
		if _, err = w.repo.EnqueueReminder(ctx, w.chatID, "tomorrow", now, reminderText(messages)); err != nil {
			return err
		}
	}
	if w.weekly && now.Weekday() == time.Sunday {
		messages, err := w.formatter.ReminderNextWeek(ctx, now)
		if err != nil {
			return err
		}
		_, err = w.repo.EnqueueReminder(ctx, w.chatID, "next_week", now, reminderText(messages))
		return err
	}
	return nil
}

func reminderText(messages []string) string {
	text := strings.Join(messages, "\n\n")
	if len([]rune(text)) <= 4096 {
		return text
	}
	suffix := "\n\nРасписание длинное; полный список доступен по команде /week."
	text = ""
	for _, message := range messages {
		candidate := message
		if text != "" {
			candidate = text + "\n\n" + message
		}
		if len([]rune(candidate+suffix)) > 4096 {
			break
		}
		text = candidate
	}
	return text + suffix
}
func (w *ReminderScheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := w.Tick(ctx); err != nil && ctx.Err() == nil {
				w.logger.Warn("schedule reminder failed", "error", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

type ReminderDeliveryWorker struct {
	repo   ReminderRepository
	sender NotificationSender
	poll   time.Duration
	logger *slog.Logger
	now    func() time.Time
}

func NewReminderDeliveryWorker(repo ReminderRepository, sender NotificationSender, poll time.Duration, logger *slog.Logger) (*ReminderDeliveryWorker, error) {
	if repo == nil || sender == nil || poll <= 0 {
		return nil, fmt.Errorf("invalid reminder delivery configuration")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ReminderDeliveryWorker{repo: repo, sender: sender, poll: poll, logger: logger, now: time.Now}, nil
}
func (w *ReminderDeliveryWorker) ProcessOne(ctx context.Context) (bool, error) {
	now := w.now()
	d, err := w.repo.ClaimReminder(ctx, now, time.Minute)
	if errors.Is(err, storage.ErrNoDelivery) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	id, sendErr := w.sender.SendNotification(ctx, d.ChatID, d.Text)
	if sendErr == nil {
		return true, w.repo.CompleteReminder(ctx, d.ID, id, w.now())
	}
	delay, retry := telegram.RetryAfter(sendErr)
	if !retry && telegram.Temporary(sendErr) {
		shift := min(max(d.Attempts-1, 0), 6)
		delay = time.Minute * time.Duration(1<<shift)
		retry = true
	}
	if err := w.repo.RetryReminder(ctx, d.ID, now.Add(delay), sendErr.Error(), !retry); err != nil {
		return true, err
	}
	return true, sendErr
}
func (w *ReminderDeliveryWorker) Run(ctx context.Context) {
	for {
		processed, err := w.ProcessOne(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			w.logger.Warn("reminder delivery failed", "error", err)
		}
		if processed {
			continue
		}
		timer := time.NewTimer(w.poll)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}
