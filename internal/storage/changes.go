package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/changes"
	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/jackc/pgx/v5"
)

const ConfirmationDelay = 10 * time.Minute

type ObservationStatus string

const (
	ObservationUnchanged   ObservationStatus = "unchanged"
	ObservationPending     ObservationStatus = "pending"
	ObservationConfirmed   ObservationStatus = "confirmed"
	ObservationQuarantined ObservationStatus = "quarantined"
	ObservationRebaselined ObservationStatus = "rebaselined"
)

type ObservationResult struct {
	Status    ObservationStatus
	ChangeSet *changes.ChangeSet
	Weeks     []time.Time
}

type Quarantine struct {
	GroupID   int64
	Monday    time.Time
	FirstSeen time.Time
	LastSeen  time.Time
	Reason    string
}

type ObserveOptions struct {
	ConfirmationDelay time.Duration
	DeliveryChatID    int64
}

// ObserveBatch records one complete successful source pass. A changed snapshot
// becomes active only after the same content is observed at least ten minutes
// after its first observation. All writes for the pass are atomic.
func (s *Store) ObserveBatch(ctx context.Context, weeks []schedule.Schedule, observedAt time.Time) (ObservationResult, error) {
	return s.ObserveBatchWithOptions(ctx, weeks, observedAt, ObserveOptions{ConfirmationDelay: ConfirmationDelay})
}

func (s *Store) ObserveBatchWithOptions(ctx context.Context, weeks []schedule.Schedule, observedAt time.Time, options ObserveOptions) (ObservationResult, error) {
	var result ObservationResult
	if len(weeks) == 0 || observedAt.IsZero() || options.ConfirmationDelay < ConfirmationDelay {
		return result, fmt.Errorf("observation batch, time and confirmation delay of at least 10m are required")
	}
	ordered := append([]schedule.Schedule(nil), weeks...)
	seen := map[string]bool{}
	for _, week := range ordered {
		if week.GroupID != ordered[0].GroupID {
			return result, fmt.Errorf("observation batch must contain one group")
		}
		if err := s.validate(week); err != nil {
			return result, err
		}
		key := week.Monday.Format(time.DateOnly)
		if seen[key] {
			return result, fmt.Errorf("duplicate week in observation batch")
		}
		seen[key] = true
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Monday.Before(ordered[j].Monday) })
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin observation: %w", err)
	}
	defer rollback(tx)
	lockKey := "lunara:changes:" + strconv.FormatInt(ordered[0].GroupID, 10)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return result, err
	}

	var allChanges []changes.Change
	var confirmedWeeks []time.Time
	var historyWeeks []time.Time
	allHistoryFirstPublication := true
	anyPending, anyQuarantine, anyRebaseline := false, false, false
	for _, candidate := range ordered {
		active, err := getWeekTx(ctx, tx, s.calendar, candidate.GroupID, candidate.Monday)
		if err != nil {
			return result, fmt.Errorf("read active snapshot: %w", err)
		}
		if active.Hash == candidate.Hash && active.NormalizationVersion == candidate.NormalizationVersion {
			if _, err := tx.Exec(ctx, `DELETE FROM schedule_candidates WHERE group_id=$1 AND week_start=$2::date`, candidate.GroupID, candidate.Monday.Format(time.DateOnly)); err != nil {
				return result, err
			}
			continue
		}
		snapshotID, err := saveSnapshotTx(ctx, tx, candidate)
		if err != nil {
			return result, err
		}
		var pendingID int64
		var firstSeen time.Time
		var quarantined bool
		err = tx.QueryRow(ctx, `SELECT snapshot_id,first_seen_at,quarantined FROM schedule_candidates WHERE group_id=$1 AND week_start=$2::date FOR UPDATE`, candidate.GroupID, candidate.Monday.Format(time.DateOnly)).Scan(&pendingID, &firstSeen, &quarantined)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && pendingID != snapshotID {
			_, err = tx.Exec(ctx, `INSERT INTO schedule_candidates
 (group_id,week_start,snapshot_id,first_seen_at,last_seen_at,observation_count)
 VALUES ($1,$2::date,$3,$4,$4,1)
 ON CONFLICT (group_id,week_start) DO UPDATE SET snapshot_id=EXCLUDED.snapshot_id,
 first_seen_at=EXCLUDED.first_seen_at,last_seen_at=EXCLUDED.last_seen_at,observation_count=1,
 quarantined=false,quarantine_reason=''`,
				candidate.GroupID, candidate.Monday.Format(time.DateOnly), snapshotID, observedAt.UTC())
			if err != nil {
				return result, fmt.Errorf("save pending candidate: %w", err)
			}
			anyPending = true
			continue
		}
		if err != nil {
			return result, fmt.Errorf("read pending candidate: %w", err)
		}
		if observedAt.UTC().Before(firstSeen) {
			return result, fmt.Errorf("observation time moved backwards")
		}
		if quarantined {
			anyQuarantine = true
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE schedule_candidates SET last_seen_at=$3,observation_count=observation_count+1 WHERE group_id=$1 AND week_start=$2::date`, candidate.GroupID, candidate.Monday.Format(time.DateOnly), observedAt.UTC()); err != nil {
			return result, err
		}
		if observedAt.UTC().Sub(firstSeen) < options.ConfirmationDelay {
			anyPending = true
			continue
		}

		suppress := active.NormalizationVersion != candidate.NormalizationVersion
		if !suppress && massRemoval(active, candidate) {
			reason := fmt.Sprintf("lesson count decreased from %d to %d", len(active.Lessons), len(candidate.Lessons))
			if _, err := tx.Exec(ctx, `UPDATE schedule_candidates SET quarantined=true,quarantine_reason=$3 WHERE group_id=$1 AND week_start=$2::date`, candidate.GroupID, candidate.Monday.Format(time.DateOnly), reason); err != nil {
				return result, err
			}
			anyQuarantine = true
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE schedule_heads SET snapshot_id=$3 WHERE group_id=$1 AND week_start=$2::date`, candidate.GroupID, candidate.Monday.Format(time.DateOnly), snapshotID); err != nil {
			return result, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM schedule_candidates WHERE group_id=$1 AND week_start=$2::date`, candidate.GroupID, candidate.Monday.Format(time.DateOnly)); err != nil {
			return result, err
		}
		confirmedWeeks = append(confirmedWeeks, candidate.Monday)
		if suppress {
			anyRebaseline = true
			continue
		}
		historyWeeks = append(historyWeeks, candidate.Monday)
		weekChanges := changes.Diff(active.Lessons, candidate.Lessons)
		if active.Status != schedule.Unpublished || candidate.Status != schedule.Published {
			allHistoryFirstPublication = false
		}
		allChanges = append(allChanges, weekChanges...)
	}

	if len(confirmedWeeks) > 0 {
		setKind := changes.Regular
		if len(historyWeeks) == 0 {
			setKind = changes.ParserRebaseline
		} else if allHistoryFirstPublication {
			setKind = changes.FirstPublication
		}
		set := changes.ChangeSet{GroupID: ordered[0].GroupID, DetectedAt: observedAt.UTC(), WeekStarts: historyWeeks, Kind: setKind, Changes: allChanges}
		set.Sort()
		if len(historyWeeks) > 0 {
			if err := insertChangeSetTx(ctx, tx, &set); err != nil {
				return result, err
			}
			if options.DeliveryChatID != 0 {
				if err := enqueueDeliveryTx(ctx, tx, options.DeliveryChatID, set.ID, observedAt.UTC()); err != nil {
					return result, err
				}
			}
			result.ChangeSet = &set
		}
		result.Weeks = confirmedWeeks
		if anyRebaseline && len(historyWeeks) == 0 {
			result.Status = ObservationRebaselined
		} else {
			result.Status = ObservationConfirmed
		}
	} else if anyQuarantine {
		result.Status = ObservationQuarantined
	} else if anyPending {
		result.Status = ObservationPending
	} else {
		result.Status = ObservationUnchanged
	}
	if err := tx.Commit(ctx); err != nil {
		return ObservationResult{}, fmt.Errorf("commit observation: %w", err)
	}
	return result, nil
}

// ResetCandidates records that a complete confirmation sequence was broken by
// an error. Quarantined candidates remain until an operator decides on them.
func (s *Store) ResetCandidates(ctx context.Context, groupID int64, weeks []time.Time) error {
	if groupID <= 0 || len(weeks) == 0 {
		return fmt.Errorf("group and weeks are required")
	}
	for _, week := range weeks {
		if week.IsZero() {
			return fmt.Errorf("week is required")
		}
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM schedule_candidates WHERE group_id=$1 AND NOT quarantined AND week_start=ANY($2::date[])`, groupID, dateStrings(weeks))
	if err != nil {
		return fmt.Errorf("reset candidates: %w", err)
	}
	return nil
}

func (s *Store) ChangeSets(ctx context.Context, groupID int64, beforeID int64, limit int) ([]changes.ChangeSet, error) {
	if groupID <= 0 || limit < 1 || limit > 20 || beforeID < 0 {
		return nil, fmt.Errorf("invalid change history query")
	}
	rows, err := s.pool.Query(ctx, `SELECT id,group_id,detected_at,week_starts,kind,changes FROM change_sets
 WHERE group_id=$1 AND ($2=0 OR id<$2) ORDER BY id DESC LIMIT $3`, groupID, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("read change history: %w", err)
	}
	defer rows.Close()
	var sets []changes.ChangeSet
	for rows.Next() {
		var set changes.ChangeSet
		var dates []time.Time
		var kind string
		var body []byte
		if err := rows.Scan(&set.ID, &set.GroupID, &set.DetectedAt, &dates, &kind, &body); err != nil {
			return nil, err
		}
		set.Kind = changes.SetKind(kind)
		if err := json.Unmarshal(body, &set.Changes); err != nil {
			return nil, err
		}
		for _, date := range dates {
			set.WeekStarts = append(set.WeekStarts, schedule.LocalDate(date, s.calendar.Location))
		}
		sets = append(sets, set)
	}
	return sets, rows.Err()
}

func (s *Store) Quarantines(ctx context.Context, groupID int64) ([]Quarantine, error) {
	if groupID <= 0 {
		return nil, fmt.Errorf("positive group ID is required")
	}
	rows, err := s.pool.Query(ctx, `SELECT group_id,week_start,first_seen_at,last_seen_at,quarantine_reason
 FROM schedule_candidates WHERE group_id=$1 AND quarantined ORDER BY week_start`, groupID)
	if err != nil {
		return nil, fmt.Errorf("read quarantines: %w", err)
	}
	defer rows.Close()
	var result []Quarantine
	for rows.Next() {
		var item Quarantine
		if err := rows.Scan(&item.GroupID, &item.Monday, &item.FirstSeen, &item.LastSeen, &item.Reason); err != nil {
			return nil, err
		}
		item.Monday = schedule.LocalDate(item.Monday, s.calendar.Location)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) AcceptQuarantine(ctx context.Context, groupID int64, date time.Time, acceptedAt time.Time) (changes.ChangeSet, error) {
	return s.AcceptQuarantineForChat(ctx, groupID, date, acceptedAt, 0)
}

func (s *Store) AcceptQuarantineForChat(ctx context.Context, groupID int64, date time.Time, acceptedAt time.Time, chatID int64) (changes.ChangeSet, error) {
	var set changes.ChangeSet
	if groupID <= 0 || date.IsZero() || acceptedAt.IsZero() {
		return set, fmt.Errorf("group, week and acceptance time are required")
	}
	monday := schedule.Monday(date, s.calendar.Location)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return set, err
	}
	defer rollback(tx)
	lockKey := "lunara:changes:" + strconv.FormatInt(groupID, 10)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return set, err
	}
	active, err := getWeekTx(ctx, tx, s.calendar, groupID, monday)
	if err != nil {
		return set, err
	}
	var snapshotID int64
	err = tx.QueryRow(ctx, `SELECT snapshot_id FROM schedule_candidates WHERE group_id=$1 AND week_start=$2::date AND quarantined FOR UPDATE`, groupID, monday.Format(time.DateOnly)).Scan(&snapshotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return set, ErrNotFound
	}
	if err != nil {
		return set, err
	}
	var candidate schedule.Schedule
	var weekType, status string
	var body []byte
	err = tx.QueryRow(ctx, `SELECT group_id,week_type,status,lessons,normalization_version,content_hash,checked_at FROM schedule_snapshots WHERE id=$1`, snapshotID).Scan(
		&candidate.GroupID, &weekType, &status, &body, &candidate.NormalizationVersion, &candidate.Hash, &candidate.CheckedAt)
	if err != nil {
		return set, err
	}
	candidate.Monday, candidate.WeekType, candidate.Status = monday, schedule.WeekType(weekType), schedule.Status(status)
	if err := json.Unmarshal(body, &candidate.Lessons); err != nil {
		return set, err
	}
	for i := range candidate.Lessons {
		candidate.Lessons[i].Date = schedule.LocalDate(candidate.Lessons[i].Date, s.calendar.Location)
	}
	set = changes.ChangeSet{GroupID: groupID, DetectedAt: acceptedAt.UTC(), WeekStarts: []time.Time{monday}, Kind: changes.Regular, Changes: changes.Diff(active.Lessons, candidate.Lessons)}
	set.Sort()
	if err := insertChangeSetTx(ctx, tx, &set); err != nil {
		return changes.ChangeSet{}, err
	}
	if chatID != 0 {
		if err := enqueueDeliveryTx(ctx, tx, chatID, set.ID, acceptedAt.UTC()); err != nil {
			return changes.ChangeSet{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE schedule_heads SET snapshot_id=$3 WHERE group_id=$1 AND week_start=$2::date`, groupID, monday.Format(time.DateOnly), snapshotID); err != nil {
		return changes.ChangeSet{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM schedule_candidates WHERE group_id=$1 AND week_start=$2::date`, groupID, monday.Format(time.DateOnly)); err != nil {
		return changes.ChangeSet{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return changes.ChangeSet{}, err
	}
	return set, nil
}

func enqueueDeliveryTx(ctx context.Context, tx pgx.Tx, chatID, changeSetID int64, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO notification_deliveries (chat_id,change_set_id,next_attempt_at)
 VALUES ($1,$2,$3) ON CONFLICT (chat_id,change_set_id) DO NOTHING`, chatID, changeSetID, now)
	return err
}

func massRemoval(old, new schedule.Schedule) bool {
	if old.Status != schedule.Published {
		return false
	}
	if new.Status == schedule.Unpublished {
		return true
	}
	removed := len(old.Lessons) - len(new.Lessons)
	return removed > 0 && removed*100 >= len(old.Lessons)*30
}

func saveSnapshotTx(ctx context.Context, tx pgx.Tx, week schedule.Schedule) (int64, error) {
	lessons, err := json.Marshal(week.Lessons)
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO schedule_snapshots
 (group_id,week_start,week_type,status,lessons,normalization_version,content_hash,checked_at)
 VALUES ($1,$2::date,$3,$4,$5::jsonb,$6,$7,$8)
 ON CONFLICT (group_id,week_start,normalization_version,content_hash)
 DO UPDATE SET checked_at=GREATEST(schedule_snapshots.checked_at,EXCLUDED.checked_at) RETURNING id`,
		week.GroupID, week.Monday.Format(time.DateOnly), week.WeekType, week.Status, lessons, week.NormalizationVersion, week.Hash, week.CheckedAt).Scan(&id)
	return id, err
}

func getWeekTx(ctx context.Context, tx pgx.Tx, calendar schedule.Calendar, groupID int64, monday time.Time) (schedule.Schedule, error) {
	var result schedule.Schedule
	var weekType, status string
	var body []byte
	err := tx.QueryRow(ctx, `SELECT s.group_id,s.week_type,s.status,s.lessons,s.normalization_version,s.content_hash,s.checked_at
 FROM schedule_heads h JOIN schedule_snapshots s ON s.id=h.snapshot_id WHERE h.group_id=$1 AND h.week_start=$2::date FOR UPDATE OF h`, groupID, monday.Format(time.DateOnly)).Scan(
		&result.GroupID, &weekType, &status, &body, &result.NormalizationVersion, &result.Hash, &result.CheckedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	result.Monday, result.WeekType, result.Status = schedule.Monday(monday, calendar.Location), schedule.WeekType(weekType), schedule.Status(status)
	if err := json.Unmarshal(body, &result.Lessons); err != nil {
		return result, err
	}
	for i := range result.Lessons {
		result.Lessons[i].Date = schedule.LocalDate(result.Lessons[i].Date, calendar.Location)
	}
	return result, nil
}

func insertChangeSetTx(ctx context.Context, tx pgx.Tx, set *changes.ChangeSet) error {
	body, err := json.Marshal(set.Changes)
	if err != nil {
		return err
	}
	return tx.QueryRow(ctx, `INSERT INTO change_sets (group_id,detected_at,week_starts,kind,changes) VALUES ($1,$2,$3::date[],$4,$5::jsonb) RETURNING id`,
		set.GroupID, set.DetectedAt, dateStrings(set.WeekStarts), set.Kind, body).Scan(&set.ID)
}

func dateStrings(dates []time.Time) []string {
	values := make([]string, len(dates))
	for i, date := range dates {
		values[i] = date.Format(time.DateOnly)
	}
	return values
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
