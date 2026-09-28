package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/changes"
	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/jackc/pgx/v5"
)

var ErrNoDelivery = errors.New("no notification delivery ready")

type Delivery struct {
	ID        int64
	ChatID    int64
	Attempts  int
	ChangeSet changes.ChangeSet
}

func (s *Store) ClaimDelivery(ctx context.Context, now time.Time, lease time.Duration) (Delivery, error) {
	var delivery Delivery
	if now.IsZero() || lease <= 0 {
		return delivery, fmt.Errorf("claim time and lease are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return delivery, err
	}
	defer rollback(tx)
	var changeSetID int64
	err = tx.QueryRow(ctx, `WITH picked AS (
 SELECT id FROM notification_deliveries
 WHERE (status='pending' AND next_attempt_at<=$1) OR (status='sending' AND lease_until<=$1)
 ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE notification_deliveries d SET status='sending',attempts=attempts+1,lease_until=$1+make_interval(secs => $2)
 FROM picked WHERE d.id=picked.id RETURNING d.id,d.chat_id,d.change_set_id,d.attempts`,
		now.UTC(), lease.Seconds()).Scan(&delivery.ID, &delivery.ChatID, &changeSetID, &delivery.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return delivery, ErrNoDelivery
	}
	if err != nil {
		return delivery, fmt.Errorf("claim notification: %w", err)
	}
	var dates []time.Time
	var kind string
	var body []byte
	err = tx.QueryRow(ctx, `SELECT id,group_id,detected_at,week_starts,kind,changes FROM change_sets WHERE id=$1`, changeSetID).Scan(
		&delivery.ChangeSet.ID, &delivery.ChangeSet.GroupID, &delivery.ChangeSet.DetectedAt, &dates, &kind, &body)
	if err != nil {
		return Delivery{}, err
	}
	delivery.ChangeSet.Kind = changes.SetKind(kind)
	if err := json.Unmarshal(body, &delivery.ChangeSet.Changes); err != nil {
		return Delivery{}, err
	}
	for _, date := range dates {
		delivery.ChangeSet.WeekStarts = append(delivery.ChangeSet.WeekStarts, schedule.LocalDate(date, s.calendar.Location))
	}
	if err := tx.Commit(ctx); err != nil {
		return Delivery{}, err
	}
	return delivery, nil
}

func (s *Store) CompleteDelivery(ctx context.Context, id, messageID int64, sentAt time.Time) error {
	if id <= 0 || messageID <= 0 || sentAt.IsZero() {
		return fmt.Errorf("delivery, message and sent time are required")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE notification_deliveries SET status='sent',telegram_message_id=$2,sent_at=$3,lease_until=NULL,last_error=''
 WHERE id=$1 AND status='sending'`, id, messageID, sentAt.UTC())
	if err != nil {
		return fmt.Errorf("complete notification: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("notification delivery is not claimed")
	}
	return nil
}

func (s *Store) RetryDelivery(ctx context.Context, id int64, next time.Time, reason string, permanent bool) error {
	if id <= 0 || next.IsZero() {
		return fmt.Errorf("delivery and retry time are required")
	}
	if len(reason) > 1000 {
		reason = reason[:1000]
	}
	status := "pending"
	if permanent {
		status = "failed"
	}
	tag, err := s.pool.Exec(ctx, `UPDATE notification_deliveries SET status=$2,next_attempt_at=$3,lease_until=NULL,last_error=$4
 WHERE id=$1 AND status='sending'`, id, status, next.UTC(), reason)
	if err != nil {
		return fmt.Errorf("retry notification: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("notification delivery is not claimed")
	}
	return nil
}
