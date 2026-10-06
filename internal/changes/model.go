// Package changes compares normalized schedules and describes confirmed changes.
package changes

import (
	"sort"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
)

type Kind string

const (
	Added    Kind = "added"
	Removed  Kind = "removed"
	Modified Kind = "modified"
)

type Field string

const (
	FieldNumber   Field = "number"
	FieldTime     Field = "time"
	FieldType     Field = "type"
	FieldTeachers Field = "teachers"
	FieldRooms    Field = "rooms"
)

type Change struct {
	Kind      Kind             `json:"kind"`
	Old       *schedule.Lesson `json:"old,omitempty"`
	New       *schedule.Lesson `json:"new,omitempty"`
	Fields    []Field          `json:"fields,omitempty"`
	Ambiguous bool             `json:"ambiguous,omitempty"`
}

type SetKind string

const (
	Regular          SetKind = "regular"
	MajorChange      SetKind = "major_change"
	FirstPublication SetKind = "first_publication"
	ParserRebaseline SetKind = "parser_rebaseline"
)

type ChangeSet struct {
	ID         int64       `json:"id"`
	GroupID    int64       `json:"group_id"`
	DetectedAt time.Time   `json:"detected_at"`
	WeekStarts []time.Time `json:"week_starts"`
	Kind       SetKind     `json:"kind"`
	Changes    []Change    `json:"changes"`
}

func (s *ChangeSet) Sort() {
	sort.Slice(s.WeekStarts, func(i, j int) bool { return s.WeekStarts[i].Before(s.WeekStarts[j]) })
	sort.SliceStable(s.Changes, func(i, j int) bool {
		a, b := representative(s.Changes[i]), representative(s.Changes[j])
		if !a.Date.Equal(b.Date) {
			return a.Date.Before(b.Date)
		}
		if a.StartMinute != b.StartMinute {
			return a.StartMinute < b.StartMinute
		}
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		return s.Changes[i].Kind < s.Changes[j].Kind
	})
}

func representative(change Change) schedule.Lesson {
	if change.New != nil {
		return *change.New
	}
	if change.Old != nil {
		return *change.Old
	}
	return schedule.Lesson{}
}
