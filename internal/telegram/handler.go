package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/LeezyWannaFall/Lunara/internal/storage"
)

type ScheduleRepository interface {
	GetWeek(context.Context, int64, time.Time) (schedule.Schedule, error)
}

type Handler struct {
	repo     ScheduleRepository
	calendar schedule.Calendar
	groupID  int64
	now      func() time.Time
}

func NewHandler(repo ScheduleRepository, calendar schedule.Calendar, groupID int64) (*Handler, error) {
	if repo == nil || groupID <= 0 {
		return nil, fmt.Errorf("schedule repository and positive group ID are required")
	}
	if err := calendar.Validate(); err != nil {
		return nil, err
	}
	return &Handler{repo: repo, calendar: calendar, groupID: groupID, now: time.Now}, nil
}

func (h *Handler) Handle(ctx context.Context, text, botUsername string) ([]string, error) {
	command, argument, ok := parseCommand(text, botUsername)
	if !ok {
		return nil, nil
	}
	now := h.now().In(h.calendar.Location)
	switch command {
	case "start", "help":
		return []string{h.help()}, nil
	case "today":
		return h.day(ctx, schedule.LocalDate(now, h.calendar.Location))
	case "tomorrow":
		return h.day(ctx, schedule.LocalDate(now, h.calendar.Location).AddDate(0, 0, 1))
	case "day":
		if argument == "" {
			return []string{"Укажите дату: <code>/day 28.09.2026</code> или <code>/day 2026-09-28</code>."}, nil
		}
		date, err := parseUserDate(argument, h.calendar.Location)
		if err != nil {
			return []string{"Не удалось распознать дату. Используйте <code>ДД.ММ.ГГГГ</code> или <code>YYYY-MM-DD</code>."}, nil
		}
		return h.day(ctx, date)
	case "week":
		return h.week(ctx, now)
	case "nextweek":
		return h.week(ctx, schedule.Monday(now, h.calendar.Location).AddDate(0, 0, 7))
	case "next":
		return h.next(ctx, now)
	case "changes":
		return []string{"История изменений появится после подключения обнаружения изменений на этапе 5."}, nil
	case "exams":
		return []string{"Просмотр экзаменов пока не подключён. Источник: https://study.miigaik.ru/exam/"}, nil
	default:
		return []string{"Неизвестная команда. Используйте /help."}, nil
	}
}

func parseCommand(text, username string) (string, string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", "", false
	}
	name := strings.TrimPrefix(fields[0], "/")
	if at := strings.IndexByte(name, '@'); at >= 0 {
		if username == "" || !strings.EqualFold(name[at+1:], username) {
			return "", "", false
		}
		name = name[:at]
	}
	return strings.ToLower(name), strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), fields[0])), true
}

func parseUserDate(value string, location *time.Location) (time.Time, error) {
	for _, layout := range []string{"02.01.2006", time.DateOnly} {
		if date, err := time.ParseInLocation(layout, value, location); err == nil {
			return date, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date")
}

func (h *Handler) day(ctx context.Context, date time.Time) ([]string, error) {
	week, err := h.repo.GetWeek(ctx, h.groupID, date)
	if errors.Is(err, storage.ErrNotFound) {
		return []string{fmt.Sprintf("За %s данных в кеше пока нет.", date.Format("02.01.2006"))}, nil
	}
	if err != nil {
		return nil, err
	}
	if week.Status == schedule.Unpublished {
		return []string{fmt.Sprintf("Расписание на неделю %s ещё не опубликовано. Последняя проверка: %s.", week.Monday.Format("02.01.2006"), checked(week.CheckedAt, h.calendar.Location))}, nil
	}
	var lessons []schedule.Lesson
	for _, lesson := range week.Lessons {
		if sameDate(lesson.Date, date, h.calendar.Location) {
			lessons = append(lessons, lesson)
		}
	}
	weekType, _ := h.calendar.WeekTypeAt(date)
	header := fmt.Sprintf("<b>%s, %s</b> · %s неделя", weekday(date), date.Format("02.01.2006"), weekName(weekType))
	if len(lessons) == 0 {
		return []string{header + "\nЗанятий нет."}, nil
	}
	return formatLessonChunks(header, lessons), nil
}

func (h *Handler) week(ctx context.Context, date time.Time) ([]string, error) {
	week, err := h.repo.GetWeek(ctx, h.groupID, date)
	if errors.Is(err, storage.ErrNotFound) {
		return []string{fmt.Sprintf("За неделю %s данных в кеше пока нет.", schedule.Monday(date, h.calendar.Location).Format("02.01.2006"))}, nil
	}
	if err != nil {
		return nil, err
	}
	if week.Status == schedule.Unpublished {
		return []string{fmt.Sprintf("Расписание на неделю %s ещё не опубликовано. Последняя проверка: %s.", week.Monday.Format("02.01.2006"), checked(week.CheckedAt, h.calendar.Location))}, nil
	}
	byDate := map[string][]schedule.Lesson{}
	for _, lesson := range week.Lessons {
		key := lesson.Date.In(h.calendar.Location).Format(time.DateOnly)
		byDate[key] = append(byDate[key], lesson)
	}
	result := []string{fmt.Sprintf("<b>Неделя %s–%s</b> · %s", week.Monday.Format("02.01"), week.Monday.AddDate(0, 0, 6).Format("02.01.2006"), weekName(week.WeekType))}
	for day := 0; day < 7; day++ {
		date := week.Monday.AddDate(0, 0, day)
		lessons := byDate[date.Format(time.DateOnly)]
		if len(lessons) > 0 {
			result = append(result, formatLessonChunks(fmt.Sprintf("<b>%s, %s</b>", weekday(date), date.Format("02.01")), lessons)...)
		}
	}
	return result, nil
}

func (h *Handler) next(ctx context.Context, now time.Time) ([]string, error) {
	var incomplete bool
	for i := 0; i < 12; i++ {
		monday := schedule.Monday(now, h.calendar.Location).AddDate(0, 0, 7*i)
		week, err := h.repo.GetWeek(ctx, h.groupID, monday)
		if errors.Is(err, storage.ErrNotFound) || err == nil && week.Status == schedule.Unpublished {
			incomplete = true
			continue
		}
		if err != nil {
			return nil, err
		}
		var candidates []schedule.Lesson
		for _, lesson := range week.Lessons {
			start := time.Date(lesson.Date.Year(), lesson.Date.Month(), lesson.Date.Day(), lesson.StartMinute/60, lesson.StartMinute%60, 0, 0, h.calendar.Location)
			if !start.Before(now) {
				candidates = append(candidates, lesson)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			return lessonStart(candidates[i], h.calendar.Location).Before(lessonStart(candidates[j], h.calendar.Location))
		})
		first := lessonStart(candidates[0], h.calendar.Location)
		var simultaneous []schedule.Lesson
		for _, lesson := range candidates {
			if lessonStart(lesson, h.calendar.Location).Equal(first) {
				simultaneous = append(simultaneous, lesson)
			}
		}
		header := fmt.Sprintf("<b>Ближайшая пара: %s, %s</b>", weekday(first), first.Format("02.01.2006"))
		messages := formatLessonChunks(header, simultaneous)
		if incomplete {
			messages[0] += "\n\n⚠️ До найденной пары есть недели без опубликованных данных; результат может быть неполным."
		}
		return messages, nil
	}
	if incomplete {
		return []string{"Ближайшая пара не найдена. В кеше есть пробелы, поэтому поиск может быть неполным."}, nil
	}
	return []string{"В ближайшие 12 недель занятий не найдено."}, nil
}

func formatLessonChunks(header string, lessons []schedule.Lesson) []string {
	chunks := []string{}
	current := header
	for _, lesson := range lessons {
		block := "\n\n" + formatLesson(lesson)
		if len([]rune(current+block)) > 4096 {
			chunks = append(chunks, current)
			current = header + " (продолжение)" + block
		} else {
			current += block
		}
	}
	return append(chunks, current)
}

func formatLesson(lesson schedule.Lesson) string {
	label := ""
	if lesson.Number != nil {
		label = fmt.Sprintf("%d пара · ", *lesson.Number)
	}
	line := fmt.Sprintf("<b>%s%s–%s</b>  %s", label, minute(lesson.StartMinute), minute(lesson.EndMinute), html.EscapeString(clip(lesson.Subject, 200)))
	meta := []string{typeName(lesson)}
	if lesson.Subgroup != "" {
		meta = append(meta, "подгруппа "+html.EscapeString(clip(lesson.Subgroup, 40)))
	}
	if len(lesson.Teachers) > 0 {
		limit := min(len(lesson.Teachers), 3)
		teachers := make([]string, limit)
		for i, teacher := range lesson.Teachers[:limit] {
			teachers[i] = html.EscapeString(clip(teacher, 40))
		}
		if len(lesson.Teachers) > limit {
			teachers = append(teachers, "…")
		}
		meta = append(meta, strings.Join(teachers, ", "))
	}
	if len(lesson.Rooms) > 0 {
		rooms := make([]string, 0, min(len(lesson.Rooms), 3))
		for _, room := range lesson.Rooms[:min(len(lesson.Rooms), 3)] {
			value := html.EscapeString(clip(room.Name, 40))
			if room.Building != "" {
				value += " (" + html.EscapeString(clip(room.Building, 40)) + ")"
			}
			rooms = append(rooms, value)
		}
		if len(lesson.Rooms) > 3 {
			rooms = append(rooms, "…")
		}
		meta = append(meta, strings.Join(rooms, ", "))
	}
	return line + "\n" + strings.Join(meta, " · ")
}

func (h *Handler) help() string {
	return fmt.Sprintf("<b>Lunara · группа %d</b>\n\n/today — сегодня\n/tomorrow — завтра\n/week — текущая неделя\n/nextweek — следующая неделя\n/next — ближайшая пара\n/day ДД.ММ.ГГГГ — выбранный день\n/changes — история изменений\n/exams — экзамены\n/help — эта справка", h.groupID)
}

func sameDate(a, b time.Time, loc *time.Location) bool {
	return schedule.LocalDate(a, loc).Equal(schedule.LocalDate(b, loc))
}
func lessonStart(l schedule.Lesson, loc *time.Location) time.Time {
	return time.Date(l.Date.Year(), l.Date.Month(), l.Date.Day(), l.StartMinute/60, l.StartMinute%60, 0, 0, loc)
}
func minute(value int) string { return fmt.Sprintf("%02d:%02d", value/60, value%60) }
func weekName(value schedule.WeekType) string {
	if value == schedule.Upper {
		return "верхняя"
	}
	return "нижняя"
}
func weekday(value time.Time) string {
	return [...]string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"}[value.Weekday()]
}
func checked(value time.Time, loc *time.Location) string {
	return value.In(loc).Format("02.01.2006 15:04")
}
func clip(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}
func typeName(l schedule.Lesson) string {
	switch l.Type {
	case schedule.Lecture:
		return "лекция"
	case schedule.Practice:
		return "практика"
	case schedule.Lab:
		return "лабораторная"
	}
	if l.RawType != "" {
		return html.EscapeString(l.RawType)
	}
	return "другое занятие"
}
