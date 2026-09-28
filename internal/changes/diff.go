package changes

import (
	"encoding/json"
	"reflect"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
)

// Diff compares lessons as multisets. A field change is reported only when the
// old/new pairing is mutually unambiguous; otherwise additions and removals are
// preserved and marked ambiguous.
func Diff(oldLessons, newLessons []schedule.Lesson) []Change {
	oldLeft, newLeft := removeExact(oldLessons, newLessons)
	pairs := map[int]int{}
	match(oldLeft, newLeft, pairs, func(a, b schedule.Lesson) bool {
		return identity(a, b) && a.StartMinute == b.StartMinute && a.EndMinute == b.EndMinute
	})
	match(oldLeft, newLeft, pairs, func(a, b schedule.Lesson) bool {
		return identity(a, b) && numberEqual(a.Number, b.Number)
	})
	match(oldLeft, newLeft, pairs, func(a, b schedule.Lesson) bool { return identity(a, b) })

	changes := make([]Change, 0, len(oldLeft)+len(newLeft))
	pairedNew := map[int]bool{}
	for oi, ni := range pairs {
		oldLesson, newLesson := oldLeft[oi], newLeft[ni]
		fields := changedFields(oldLesson, newLesson)
		if len(fields) > 0 {
			o, n := oldLesson, newLesson
			changes = append(changes, Change{Kind: Modified, Old: &o, New: &n, Fields: fields})
		}
		pairedNew[ni] = true
	}
	for oi, lesson := range oldLeft {
		if _, ok := pairs[oi]; ok {
			continue
		}
		value := lesson
		changes = append(changes, Change{Kind: Removed, Old: &value, Ambiguous: hasIdentityNew(lesson, newLeft, pairedNew)})
	}
	for ni, lesson := range newLeft {
		if pairedNew[ni] {
			continue
		}
		value := lesson
		changes = append(changes, Change{Kind: Added, New: &value, Ambiguous: hasIdentityOld(lesson, oldLeft, pairs)})
	}
	set := ChangeSet{Changes: changes}
	set.Sort()
	return set.Changes
}

func removeExact(oldLessons, newLessons []schedule.Lesson) ([]schedule.Lesson, []schedule.Lesson) {
	counts := map[string]int{}
	for _, lesson := range newLessons {
		counts[lessonKey(lesson)]++
	}
	oldLeft := make([]schedule.Lesson, 0, len(oldLessons))
	for _, lesson := range oldLessons {
		key := lessonKey(lesson)
		if counts[key] > 0 {
			counts[key]--
			continue
		}
		oldLeft = append(oldLeft, lesson)
	}
	counts = map[string]int{}
	for _, lesson := range oldLessons {
		counts[lessonKey(lesson)]++
	}
	newLeft := make([]schedule.Lesson, 0, len(newLessons))
	for _, lesson := range newLessons {
		key := lessonKey(lesson)
		if counts[key] > 0 {
			counts[key]--
			continue
		}
		newLeft = append(newLeft, lesson)
	}
	return oldLeft, newLeft
}

func lessonKey(lesson schedule.Lesson) string {
	copy := lesson
	copy.Date = schedule.LocalDate(lesson.Date, lesson.Date.Location())
	body, _ := json.Marshal(copy)
	return string(body)
}

func match(oldLessons, newLessons []schedule.Lesson, pairs map[int]int, predicate func(schedule.Lesson, schedule.Lesson) bool) {
	for {
		oldCandidates := map[int][]int{}
		newCandidates := map[int][]int{}
		usedNew := map[int]bool{}
		for _, ni := range pairs {
			usedNew[ni] = true
		}
		for oi, oldLesson := range oldLessons {
			if _, used := pairs[oi]; used {
				continue
			}
			for ni, newLesson := range newLessons {
				if usedNew[ni] || !predicate(oldLesson, newLesson) {
					continue
				}
				oldCandidates[oi] = append(oldCandidates[oi], ni)
				newCandidates[ni] = append(newCandidates[ni], oi)
			}
		}
		matched := false
		for oi, candidates := range oldCandidates {
			if len(candidates) != 1 || len(newCandidates[candidates[0]]) != 1 {
				continue
			}
			pairs[oi] = candidates[0]
			matched = true
		}
		if !matched {
			return
		}
	}
}

func identity(a, b schedule.Lesson) bool {
	return a.Date.Equal(b.Date) && a.Subject == b.Subject && a.Subgroup == b.Subgroup
}

func numberEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func changedFields(a, b schedule.Lesson) []Field {
	fields := []Field{}
	if !numberEqual(a.Number, b.Number) {
		fields = append(fields, FieldNumber)
	}
	if a.StartMinute != b.StartMinute || a.EndMinute != b.EndMinute {
		fields = append(fields, FieldTime)
	}
	if a.Type != b.Type || a.RawType != b.RawType {
		fields = append(fields, FieldType)
	}
	if !reflect.DeepEqual(a.Teachers, b.Teachers) {
		fields = append(fields, FieldTeachers)
	}
	if !reflect.DeepEqual(a.Rooms, b.Rooms) {
		fields = append(fields, FieldRooms)
	}
	return fields
}

func hasIdentityNew(old schedule.Lesson, lessons []schedule.Lesson, paired map[int]bool) bool {
	for i, lesson := range lessons {
		if !paired[i] && identity(old, lesson) {
			return true
		}
	}
	return false
}

func hasIdentityOld(new schedule.Lesson, lessons []schedule.Lesson, paired map[int]int) bool {
	for i, lesson := range lessons {
		if _, ok := paired[i]; !ok && identity(lesson, new) {
			return true
		}
	}
	return false
}
