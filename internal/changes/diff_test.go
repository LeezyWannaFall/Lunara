package changes

import (
	"reflect"
	"testing"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
)

func lesson(subject string, start int) schedule.Lesson {
	return schedule.Lesson{Date: time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC), StartMinute: start, EndMinute: start + 90, Subject: subject, Type: schedule.Lecture, Teachers: []string{}, Rooms: []schedule.Room{}}
}

func TestDiffExactMultisetAndOrder(t *testing.T) {
	a, b := lesson("A", 540), lesson("B", 700)
	if got := Diff([]schedule.Lesson{a, a, b}, []schedule.Lesson{b, a, a}); len(got) != 0 {
		t.Fatalf("reordering or duplicates changed: %+v", got)
	}
	got := Diff([]schedule.Lesson{a, a}, []schedule.Lesson{a})
	if len(got) != 1 || got[0].Kind != Removed {
		t.Fatalf("duplicate count lost: %+v", got)
	}
}

func TestDiffEveryMutableField(t *testing.T) {
	old := lesson("A", 540)
	n := 2
	old.Number = &n
	old.Teachers = []string{"Old"}
	old.Rooms = []schedule.Room{{Name: "1"}}
	newLesson := old
	n2 := 3
	newLesson.Number = &n2
	newLesson.StartMinute, newLesson.EndMinute = 600, 690
	newLesson.Type, newLesson.RawType = schedule.Other, "семинар"
	newLesson.Teachers = []string{"New"}
	newLesson.Rooms = []schedule.Room{{Name: "2"}}
	got := Diff([]schedule.Lesson{old}, []schedule.Lesson{newLesson})
	want := []Field{FieldNumber, FieldTime, FieldType, FieldTeachers, FieldRooms}
	if len(got) != 1 || got[0].Kind != Modified || !reflect.DeepEqual(got[0].Fields, want) {
		t.Fatalf("fields=%+v", got)
	}
}

func TestDiffEachMutableFieldIndividually(t *testing.T) {
	base := lesson("A", 540)
	n := 1
	base.Number = &n
	tests := []struct {
		name   string
		field  Field
		mutate func(*schedule.Lesson)
	}{
		{"number", FieldNumber, func(l *schedule.Lesson) { value := 2; l.Number = &value }},
		{"time", FieldTime, func(l *schedule.Lesson) { l.StartMinute, l.EndMinute = 600, 690 }},
		{"type", FieldType, func(l *schedule.Lesson) { l.Type, l.RawType = schedule.Other, "семинар" }},
		{"teachers", FieldTeachers, func(l *schedule.Lesson) { l.Teachers = []string{"Teacher"} }},
		{"rooms", FieldRooms, func(l *schedule.Lesson) { l.Rooms = []schedule.Room{{Name: "1"}} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			changed := base
			tc.mutate(&changed)
			got := Diff([]schedule.Lesson{base}, []schedule.Lesson{changed})
			if len(got) != 1 || !reflect.DeepEqual(got[0].Fields, []Field{tc.field}) {
				t.Fatalf("got=%+v", got)
			}
		})
	}
}

func TestDiffSubjectAndDateBecomeRemoveAdd(t *testing.T) {
	old := lesson("A", 540)
	newLesson := old
	newLesson.Subject = "B"
	got := Diff([]schedule.Lesson{old}, []schedule.Lesson{newLesson})
	if len(got) != 2 || got[0].Kind != Removed || got[1].Kind != Added {
		t.Fatalf("got=%+v", got)
	}
}

func TestDiffAmbiguousPairingIsNotInvented(t *testing.T) {
	a, b := lesson("A", 540), lesson("A", 700)
	x, y := lesson("A", 800), lesson("A", 900)
	got := Diff([]schedule.Lesson{a, b}, []schedule.Lesson{x, y})
	if len(got) != 4 {
		t.Fatalf("got=%+v", got)
	}
	for _, change := range got {
		if !change.Ambiguous || change.Kind == Modified {
			t.Fatalf("ambiguous match invented: %+v", got)
		}
	}
}

func TestDiffPairsByTimeBeforeNumber(t *testing.T) {
	a, b := lesson("A", 540), lesson("A", 700)
	n1, n2 := 1, 2
	a.Number, b.Number = &n1, &n2
	x, y := a, b
	x.Number, y.Number = &n2, &n1
	got := Diff([]schedule.Lesson{a, b}, []schedule.Lesson{x, y})
	if len(got) != 2 || got[0].Kind != Modified || got[1].Kind != Modified {
		t.Fatalf("got=%+v", got)
	}
}
