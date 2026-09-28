package storage

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/jackc/pgx/v5"
)

func (s *Store) SaveExams(ctx context.Context, value schedule.ExamSchedule) error {
	if value.GroupID <= 0 || value.CheckedAt.IsZero() || value.NormalizationVersion <= 0 || value.Exams == nil || value.Status != schedule.Published && value.Status != schedule.Unpublished || value.Status == schedule.Published && len(value.Exams) == 0 || value.Status == schedule.Unpublished && len(value.Exams) != 0 {
		return fmt.Errorf("invalid exam schedule")
	}
	if decoded, err := hex.DecodeString(value.Hash); err != nil || len(decoded) != 32 {
		return fmt.Errorf("invalid exam hash")
	}
	for _, exam := range value.Exams {
		if exam.Date.IsZero() || strings.TrimSpace(exam.Subject) == "" || exam.StartMinute < 0 || exam.StartMinute >= 24*60 {
			return fmt.Errorf("invalid exam")
		}
	}
	body, err := json.Marshal(value.Exams)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO exam_schedules(group_id,status,exams,normalization_version,content_hash,checked_at)
 VALUES($1,$2,$3::jsonb,$4,$5,$6) ON CONFLICT(group_id) DO UPDATE SET status=EXCLUDED.status,exams=EXCLUDED.exams,
 normalization_version=EXCLUDED.normalization_version,content_hash=EXCLUDED.content_hash,checked_at=EXCLUDED.checked_at,updated_at=now()`, value.GroupID, string(value.Status), body, value.NormalizationVersion, value.Hash, value.CheckedAt.UTC())
	if err != nil {
		return fmt.Errorf("save exams: %w", err)
	}
	return nil
}

func (s *Store) GetExams(ctx context.Context, groupID int64) (schedule.ExamSchedule, error) {
	var result schedule.ExamSchedule
	var body []byte
	var status string
	err := s.pool.QueryRow(ctx, `SELECT group_id,status,exams,normalization_version,content_hash,checked_at FROM exam_schedules WHERE group_id=$1`, groupID).Scan(&result.GroupID, &status, &body, &result.NormalizationVersion, &result.Hash, &result.CheckedAt)
	if err == pgx.ErrNoRows {
		return result, ErrNotFound
	}
	if err != nil {
		return result, fmt.Errorf("read exams: %w", err)
	}
	result.Status = schedule.Status(status)
	result.CheckedAt = result.CheckedAt.UTC()
	if err := json.Unmarshal(body, &result.Exams); err != nil {
		return schedule.ExamSchedule{}, fmt.Errorf("decode exams: %w", err)
	}
	for i := range result.Exams {
		result.Exams[i].Date = schedule.LocalDate(result.Exams[i].Date, s.calendar.Location)
	}
	return result, nil
}

type ReminderDelivery struct {
	ID, ChatID int64
	Attempts   int
	Text       string
}

func (s *Store) EnqueueReminder(ctx context.Context, chatID int64, kind string, period time.Time, text string) (bool, error) {
	if chatID == 0 || (kind != "tomorrow" && kind != "next_week") || period.IsZero() || strings.TrimSpace(text) == "" {
		return false, fmt.Errorf("invalid reminder")
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO reminder_deliveries(chat_id,kind,period_date,text) VALUES($1,$2,$3::date,$4) ON CONFLICT DO NOTHING`, chatID, kind, period.In(s.calendar.Location).Format(time.DateOnly), text)
	if err != nil {
		return false, fmt.Errorf("enqueue reminder: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) ClaimReminder(ctx context.Context, now time.Time, lease time.Duration) (ReminderDelivery, error) {
	var d ReminderDelivery
	err := s.pool.QueryRow(ctx, `WITH picked AS (SELECT id FROM reminder_deliveries WHERE (status='pending' AND next_attempt_at<=$1) OR (status='sending' AND lease_until<=$1) ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE reminder_deliveries d SET status='sending',attempts=attempts+1,lease_until=$1+make_interval(secs=>$2) FROM picked WHERE d.id=picked.id RETURNING d.id,d.chat_id,d.attempts,d.text`, now.UTC(), lease.Seconds()).Scan(&d.ID, &d.ChatID, &d.Attempts, &d.Text)
	if err == pgx.ErrNoRows {
		return d, ErrNoDelivery
	}
	return d, err
}
func (s *Store) CompleteReminder(ctx context.Context, id, messageID int64, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE reminder_deliveries SET status='sent',telegram_message_id=$2,sent_at=$3,lease_until=NULL,last_error='' WHERE id=$1 AND status='sending'`, id, messageID, at.UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("reminder is not claimed")
	}
	return nil
}
func (s *Store) RetryReminder(ctx context.Context, id int64, next time.Time, reason string, permanent bool) error {
	status := "pending"
	if permanent {
		status = "failed"
	}
	if len(reason) > 1000 {
		reason = reason[:1000]
	}
	tag, err := s.pool.Exec(ctx, `UPDATE reminder_deliveries SET status=$2,next_attempt_at=$3,lease_until=NULL,last_error=$4 WHERE id=$1 AND status='sending'`, id, status, next.UTC(), reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("reminder is not claimed")
	}
	return nil
}
