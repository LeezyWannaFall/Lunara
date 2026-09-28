// Package miigaik reads the public weekly group schedule without executing JavaScript.
package miigaik

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/PuerkitoBio/goquery"
)

const MaxResponseBytes int64 = 2 << 20
const NormalizationVersion = 1

var (
	ErrUnexpectedPage   = errors.New("unexpected schedule page")
	ErrResponseTooLarge = errors.New("schedule response exceeds size limit")
)

// ParseWeek is pure: it does not fetch URLs or set CheckedAt. On any parsing
// failure it returns a zero Schedule, never a partially populated week.
// Only complete HTML pages are accepted, not HTMX fragments.
func ParseWeek(reader io.Reader, groupID int64, monday time.Time, calendar schedule.Calendar) (schedule.Schedule, error) {
	if err := validateRequest(groupID, monday, calendar); err != nil {
		return schedule.Schedule{}, err
	}
	body, err := io.ReadAll(io.LimitReader(reader, MaxResponseBytes+1))
	if err != nil {
		return schedule.Schedule{}, fmt.Errorf("read schedule: %w", err)
	}
	if int64(len(body)) > MaxResponseBytes {
		return schedule.Schedule{}, ErrResponseTooLarge
	}
	result, err := parsePage(body, groupID, monday, calendar)
	if err != nil {
		return schedule.Schedule{}, fmt.Errorf("%w: %v", ErrUnexpectedPage, err)
	}
	return result, nil
}

func validateRequest(groupID int64, monday time.Time, calendar schedule.Calendar) error {
	if err := calendar.Validate(); err != nil {
		return err
	}
	if groupID <= 0 {
		return fmt.Errorf("group ID must be positive")
	}
	if monday.IsZero() || !monday.Equal(schedule.Monday(monday, calendar.Location)) {
		return fmt.Errorf("requested week must start at local midnight on Monday")
	}
	return nil
}

func parsePage(body []byte, groupID int64, monday time.Time, calendar schedule.Calendar) (schedule.Schedule, error) {
	text := strings.ToLower(strings.TrimSpace(string(body)))
	// net/html repairs truncated markup. Reject incomplete responses before it does.
	if !strings.HasSuffix(text, "</html>") || !strings.Contains(text, "</body>") {
		return schedule.Schedule{}, fmt.Errorf("incomplete HTML document")
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return schedule.Schedule{}, err
	}
	config := doc.Find("#schedule-group-select-config")
	if config.Length() != 1 {
		return schedule.Schedule{}, fmt.Errorf("missing group selector")
	}
	var selected struct {
		OptionsProps struct {
			InitialOptions []struct {
				Name, Value string
				Selected    bool
			}
		}
	}
	if err := json.Unmarshal([]byte(config.Text()), &selected); err != nil {
		return schedule.Schedule{}, fmt.Errorf("invalid group selector JSON")
	}
	var groupName string
	count := 0
	for _, option := range selected.OptionsProps.InitialOptions {
		if option.Selected {
			count++
			if option.Value != strconv.FormatInt(groupID, 10) {
				return schedule.Schedule{}, fmt.Errorf("selected group differs from request")
			}
			groupName = clean(option.Name)
		}
	}
	if count != 1 || groupName == "" {
		return schedule.Schedule{}, fmt.Errorf("group is not selected")
	}
	root := doc.Find("#group-schedule-results")
	if root.Length() != 1 || root.ChildrenFiltered("h2").Length() != 1 || clean(root.ChildrenFiltered("h2").Text()) != groupName {
		return schedule.Schedule{}, fmt.Errorf("invalid group results heading")
	}
	block := root.ChildrenFiltered(".schedule-block")
	if block.Length() != 1 {
		return schedule.Schedule{}, fmt.Errorf("missing schedule block")
	}
	weekType, _ := calendar.WeekTypeAt(monday)
	result := schedule.Schedule{GroupID: groupID, Monday: schedule.Monday(monday, calendar.Location), WeekType: weekType, Status: schedule.Published, Lessons: []schedule.Lesson{}, NormalizationVersion: NormalizationVersion}
	days := block.ChildrenFiltered(".day-block")
	if days.Length() == 0 || block.Find(".day-block").Length() != days.Length() {
		return schedule.Schedule{}, fmt.Errorf("invalid day blocks")
	}
	empty := block.Find(".schedule-empty")
	if empty.Length() > 0 {
		if empty.Length() != 1 || days.Length() != 1 || !days.HasClass("schedule-empty") || doc.Find(".lesson-block").Length() != 0 || clean(empty.Text()) != "Нет данных для отображения. Выберите группу или другую неделю." {
			return schedule.Schedule{}, fmt.Errorf("ambiguous empty schedule")
		}
		result.Status = schedule.Unpublished
		return fingerprint(result), nil
	}
	weekdays := [...]string{"Воскресенье", "Понедельник", "Вторник", "Среда", "Четверг", "Пятница", "Суббота"}
	seen := map[string]bool{}
	for _, day := range days.EachIter() {
		header := day.ChildrenFiltered(".weekday-block")
		if header.Length() != 1 || header.ChildrenFiltered("span").Length() != 1 {
			return schedule.Schedule{}, fmt.Errorf("missing day date")
		}
		date, err := time.ParseInLocation("02.01.2006", clean(header.ChildrenFiltered("span").Text()), calendar.Location)
		if err != nil || date.Before(monday) || !date.Before(monday.AddDate(0, 0, 7)) {
			return schedule.Schedule{}, fmt.Errorf("day date outside requested week or invalid")
		}
		if clean(day.AttrOr("data-weekday", "")) != weekdays[date.Weekday()] || clean(header.ChildrenFiltered("h3").Text()) != weekdays[date.Weekday()] {
			return schedule.Schedule{}, fmt.Errorf("day name disagrees with date")
		}
		key := date.Format(time.DateOnly)
		if seen[key] {
			return schedule.Schedule{}, fmt.Errorf("duplicate day %s", key)
		}
		seen[key] = true
		cards := day.ChildrenFiltered(".lesson-block")
		if cards.Length() == 0 {
			return schedule.Schedule{}, fmt.Errorf("day has no recognizable lessons")
		}
		for _, card := range cards.EachIter() {
			lesson, err := parseLesson(card, date)
			if err != nil {
				return schedule.Schedule{}, fmt.Errorf("%s: %w", key, err)
			}
			result.Lessons = append(result.Lessons, lesson)
		}
	}
	if len(result.Lessons) != doc.Find(".lesson-block").Length() {
		return schedule.Schedule{}, fmt.Errorf("unparsed lesson blocks")
	}
	return fingerprint(result), nil
}

func parseLesson(card *goquery.Selection, date time.Time) (schedule.Lesson, error) {
	lesson := schedule.Lesson{Date: date, Teachers: []string{}, Rooms: []schedule.Room{}}
	var err error
	lesson.StartMinute, err = parseTime(card.AttrOr("data-lesson-start", ""))
	if err != nil {
		return lesson, err
	}
	lesson.EndMinute, err = parseTime(card.AttrOr("data-lesson-end", ""))
	if err != nil || lesson.EndMinute <= lesson.StartMinute {
		return lesson, fmt.Errorf("invalid lesson time range")
	}
	left, right := card.ChildrenFiltered(".lesson-left"), card.ChildrenFiltered(".lesson-right")
	if left.Length() != 1 || right.Length() != 1 || left.ChildrenFiltered("h4").Length() != 1 {
		return lesson, fmt.Errorf("missing lesson structure")
	}
	lesson.Subject = clean(left.ChildrenFiltered("h4").Text())
	if lesson.Subject == "" {
		return lesson, fmt.Errorf("missing subject")
	}
	nums := left.Find(".lesson-num")
	if nums.Length() > 1 {
		return lesson, fmt.Errorf("multiple lesson numbers")
	}
	if nums.Length() == 1 {
		n, err := strconv.Atoi(clean(nums.Text()))
		if err != nil || n <= 0 {
			return lesson, fmt.Errorf("invalid lesson number")
		}
		lesson.Number = &n
	}
	metadata := left.ChildrenFiltered("p")
	if metadata.Length() == 0 || clean(metadata.First().Text()) == "" {
		return lesson, fmt.Errorf("missing lesson type")
	}
	rawType := clean(metadata.First().Text())
	switch strings.ToLower(rawType) {
	case "лекционные занятия", "лекция", "лекции":
		lesson.Type = schedule.Lecture
	case "практические занятия", "практика":
		lesson.Type = schedule.Practice
	case "лабораторные занятия", "лабораторные работы", "лабораторная работа":
		lesson.Type = schedule.Lab
	default:
		lesson.Type, lesson.RawType = schedule.Other, rawType
	}
	for _, extra := range metadata.Slice(1, metadata.Length()).EachIter() {
		text := clean(extra.Text())
		prefix := "подгруппа"
		if !strings.HasPrefix(strings.ToLower(text), prefix) || lesson.Subgroup != "" {
			return lesson, fmt.Errorf("unrecognized lesson metadata")
		}
		lesson.Subgroup = clean(strings.TrimLeft(text[len(prefix):], " :"))
		if lesson.Subgroup == "" {
			return lesson, fmt.Errorf("empty subgroup")
		}
	}
	for _, span := range right.ChildrenFiltered("span").EachIter() {
		cloned := span.Clone()
		cloned.Find("br").ReplaceWithHtml(";")
		for _, name := range strings.Split(cloned.Text(), ";") {
			if name = clean(name); name != "" {
				lesson.Teachers = append(lesson.Teachers, name)
			}
		}
	}
	sort.Strings(lesson.Teachers)
	for _, roomNode := range right.ChildrenFiltered(".aud-info-wrap").EachIter() {
		room := schedule.Room{}
		for _, field := range roomNode.Find(".aud-popup").ChildrenFiltered("p").EachIter() {
			value := clean(field.Text())
			if strings.HasPrefix(value, "Аудитория:") {
				room.Name = clean(strings.TrimPrefix(value, "Аудитория:"))
			}
			if strings.HasPrefix(value, "Корпус:") {
				room.Building = clean(strings.TrimPrefix(value, "Корпус:"))
			}
		}
		if room.Name == "" {
			room.Name = clean(strings.TrimPrefix(clean(roomNode.Find(".aud-num").Text()), "Аудитория"))
		}
		if room.Name != "" || room.Building != "" {
			lesson.Rooms = append(lesson.Rooms, room)
		}
	}
	sort.Slice(lesson.Rooms, func(i, j int) bool {
		a, b := lesson.Rooms[i], lesson.Rooms[j]
		if a.Building != b.Building {
			return a.Building < b.Building
		}
		return a.Name < b.Name
	})
	return lesson, nil
}

func clean(value string) string { return strings.Join(strings.Fields(value), " ") }

func parseTime(value string) (int, error) {
	t, err := time.Parse("15:04", value)
	if err != nil || t.Format("15:04") != value {
		return 0, fmt.Errorf("invalid lesson time %q", value)
	}
	return t.Hour()*60 + t.Minute(), nil
}

func fingerprint(result schedule.Schedule) schedule.Schedule {
	// JSON is used only as a stable serialization of typed, normalized lessons.
	// Duplicates are intentionally preserved. CheckedAt is still zero here.
	sort.SliceStable(result.Lessons, func(i, j int) bool {
		a, b := result.Lessons[i], result.Lessons[j]
		if !a.Date.Equal(b.Date) {
			return a.Date.Before(b.Date)
		}
		if a.StartMinute != b.StartMinute {
			return a.StartMinute < b.StartMinute
		}
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		return string(ja) < string(jb)
	})
	body, _ := json.Marshal(result)
	hash := sha256.Sum256(body)
	result.Hash = hex.EncodeToString(hash[:])
	return result
}
