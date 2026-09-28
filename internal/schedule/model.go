package schedule

import "time"

type WeekType string

const (
	Upper WeekType = "upper"
	Lower WeekType = "lower"
)

type LessonType string

const (
	Lecture  LessonType = "lecture"
	Practice LessonType = "practice"
	Lab      LessonType = "lab"
	Other    LessonType = "other"
)

// Calendar defines the alternating weeks without imposing a date range.
type Calendar struct {
	Location     *time.Location
	AnchorMonday time.Time
	AnchorType   WeekType
}

type Room struct {
	Name     string
	Building string
}

// Lesson dates are local midnight; times are local minutes since midnight.
type Lesson struct {
	Date                   time.Time
	Number                 *int
	StartMinute, EndMinute int
	Subject                string
	Type                   LessonType
	RawType                string
	Teachers               []string
	Rooms                  []Room
	Subgroup               string
}

type Status string

const (
	Published   Status = "published"
	Unpublished Status = "unpublished"
)

type Schedule struct {
	GroupID              int64
	Monday               time.Time
	WeekType             WeekType
	Lessons              []Lesson
	Status               Status
	CheckedAt            time.Time
	NormalizationVersion int
	Hash                 string
}
