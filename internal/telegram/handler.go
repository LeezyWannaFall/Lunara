package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"

	changeModel "github.com/LeezyWannaFall/Lunara/internal/changes"
	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/LeezyWannaFall/Lunara/internal/storage"
)

type ScheduleRepository interface {
	GetWeek(context.Context, int64, time.Time) (schedule.Schedule, error)
	ChangeSets(context.Context, int64, int64, int) ([]changeModel.ChangeSet, error)
}

type ExamRepository interface {
	GetExams(context.Context, int64) (schedule.ExamSchedule, error)
}

type Handler struct {
	repo     ScheduleRepository
	exams    ExamRepository
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
	exams, _ := repo.(ExamRepository)
	return &Handler{repo: repo, exams: exams, calendar: calendar, groupID: groupID, now: time.Now}, nil
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
			return []string{"📅 <b>Расписание на выбранный день</b>\n\nУкажите дату:\n<code>/day 28.09.2026</code>\nили <code>/day 2026-09-28</code>"}, nil
		}
		date, err := parseUserDate(argument, h.calendar.Location)
		if err != nil {
			return []string{"⚠️ <b>Не удалось распознать дату</b>\n\nИспользуйте формат <code>ДД.ММ.ГГГГ</code> или <code>YYYY-MM-DD</code>."}, nil
		}
		return h.day(ctx, date)
	case "week":
		return h.week(ctx, now)
	case "nextweek":
		return h.week(ctx, schedule.Monday(now, h.calendar.Location).AddDate(0, 0, 7))
	case "next":
		return h.next(ctx, now)
	case "changes":
		messages, _, err := h.Changes(ctx, argument)
		return messages, err
	case "exams":
		return h.examSchedule(ctx)
	default:
		return []string{"🤔 <b>Неизвестная команда</b>\n\nСписок доступных команд: /help"}, nil
	}
}

func (h *Handler) examSchedule(ctx context.Context) ([]string, error) {
	if h.exams == nil {
		return []string{"⏳ <b>Экзамены ещё загружаются</b>\n\nПопробуйте открыть /exams немного позже."}, nil
	}
	value, err := h.exams.GetExams(ctx, h.groupID)
	if errors.Is(err, storage.ErrNotFound) {
		return []string{"⏳ <b>Экзамены ещё загружаются</b>\n\nПопробуйте открыть /exams немного позже."}, nil
	}
	if err != nil {
		return nil, err
	}
	if value.Status == schedule.Unpublished {
		return []string{fmt.Sprintf("🎓 <b>Экзамены</b>\n\nРасписание пока не опубликовано.\n🕘 Проверено: %s", checked(value.CheckedAt, h.calendar.Location))}, nil
	}
	header := fmt.Sprintf("🎓 <b>Экзамены · группа %d</b>\n🕘 Проверено: %s", h.groupID, checked(value.CheckedAt, h.calendar.Location))
	result := []string{}
	current := header
	for _, exam := range value.Exams {
		block := "\n\n" + formatExam(exam)
		if len([]rune(current+block)) > 4096 {
			result = append(result, current)
			current = header + " (продолжение)" + block
		} else {
			current += block
		}
	}
	return append(result, current), nil
}

func (h *Handler) ReminderTomorrow(ctx context.Context, now time.Time) ([]string, error) {
	messages, err := h.day(ctx, schedule.LocalDate(now, h.calendar.Location).AddDate(0, 0, 1))
	if len(messages) > 0 {
		messages[0] = "<b>Расписание на завтра</b>\n\n" + messages[0]
	}
	return messages, err
}
func (h *Handler) ReminderNextWeek(ctx context.Context, now time.Time) ([]string, error) {
	messages, err := h.week(ctx, schedule.Monday(now, h.calendar.Location).AddDate(0, 0, 7))
	if len(messages) > 0 {
		messages[0] = "<b>Расписание на следующую неделю</b>\n\n" + messages[0]
	}
	return messages, err
}

func formatExam(exam schedule.Exam) string {
	line := fmt.Sprintf("📅 <b>%s, %s</b>\n⏰ <b>%s</b>\n📚 %s", weekdayTitle(exam.Date), humanDate(exam.Date), minute(exam.StartMinute), html.EscapeString(clip(exam.Subject, 200)))
	meta := []string{}
	if exam.Kind != "" {
		meta = append(meta, "📝 "+html.EscapeString(clip(exam.Kind, 60)))
	}
	if len(exam.Teachers) > 0 {
		meta = append(meta, "👤 "+html.EscapeString(strings.Join(exam.Teachers, ", ")))
	}
	if len(exam.Rooms) > 0 {
		rooms := make([]string, 0, len(exam.Rooms))
		for _, room := range exam.Rooms {
			v := room.Name
			if room.Building != "" {
				v += " (" + room.Building + ")"
			}
			rooms = append(rooms, html.EscapeString(v))
		}
		meta = append(meta, "📍 "+strings.Join(rooms, ", "))
	}
	if len(meta) > 0 {
		return line + "\n" + strings.Join(meta, "\n")
	}
	return line
}

// Changes returns one history page and the cursor for the next older page.
func (h *Handler) Changes(ctx context.Context, argument string) ([]string, int64, error) {
	var before int64
	if argument != "" {
		value, err := strconv.ParseInt(argument, 10, 64)
		if err != nil || value <= 0 {
			return []string{"⚠️ Используйте <code>/changes</code> без аргументов."}, 0, nil
		}
		before = value
	}
	sets, err := h.repo.ChangeSets(ctx, h.groupID, before, 2)
	if err != nil {
		return nil, 0, err
	}
	if len(sets) == 0 {
		return []string{"✨ <b>Изменений пока нет</b>\n\nПодтверждённые изменения расписания появятся здесь."}, 0, nil
	}
	set := sets[0]
	texts := formatChangeSet(set, h.calendar.Location)
	var next int64
	if len(sets) > 1 {
		next = set.ID
	}
	return texts, next, nil
}

func formatChangeSet(set changeModel.ChangeSet, loc *time.Location) []string {
	header := fmt.Sprintf("🔔 <b>Изменения в расписании</b>\n🕘 %s", set.DetectedAt.In(loc).Format("02.01.2006 · 15:04"))
	if set.Kind == changeModel.FirstPublication {
		dates := make([]string, len(set.WeekStarts))
		for i, week := range set.WeekStarts {
			dates[i] = week.Format("02.01.2006")
		}
		return []string{header + "\n\n✅ Опубликовано расписание на недели:\n" + strings.Join(dates, ", ")}
	}
	result := []string{}
	current := header
	for _, change := range set.Changes {
		block := formatChange(change)
		if len([]rune(block)) > 3800 {
			block = compactChange(change)
		}
		if len([]rune(current))+len([]rune(block))+2 > 4096 {
			result = append(result, current)
			current = header + " (продолжение)\n\n" + block
		} else {
			current += "\n\n" + block
		}
	}
	return append(result, current)
}

// NotificationText always produces one Telegram message. Large change sets are
// summarized; full details remain available through /changes.
func NotificationText(set changeModel.ChangeSet, loc *time.Location) string {
	pages := formatChangeSet(set, loc)
	if len(pages) == 1 {
		return pages[0]
	}
	counts := map[changeModel.Kind]int{}
	for _, change := range set.Changes {
		counts[change.Kind]++
	}
	return fmt.Sprintf("🔔 <b>Изменения в расписании</b>\n🕘 %s\n\n➕ Добавлено: %d\n✏️ Изменено: %d\n➖ Убрано: %d\n\nПодробности: /changes",
		set.DetectedAt.In(loc).Format("02.01.2006 15:04"), counts[changeModel.Added], counts[changeModel.Modified], counts[changeModel.Removed])
}

func compactChange(change changeModel.Change) string {
	lesson := change.New
	if lesson == nil {
		lesson = change.Old
	}
	mark := map[changeModel.Kind]string{changeModel.Added: "➕", changeModel.Removed: "➖", changeModel.Modified: "✏️"}[change.Kind]
	return fmt.Sprintf("%s <b>%s</b> · %s · %s", mark, change.Kind, lesson.Date.Format("02.01.2006"), html.EscapeString(clip(lesson.Subject, 100)))
}

func formatChange(change changeModel.Change) string {
	mark := ""
	if change.Ambiguous {
		mark = "\n\n⚠️ <i>Связь между занятиями неоднозначна.</i>"
	}
	switch change.Kind {
	case changeModel.Added:
		return "➕ <b>Добавлено занятие</b>\n\n" + formatLesson(*change.New) + mark
	case changeModel.Removed:
		return "➖ <b>Убрано из расписания</b>\n\n" + formatLesson(*change.Old) + mark
	case changeModel.Modified:
		labels := make([]string, len(change.Fields))
		for i, field := range change.Fields {
			labels[i] = fieldName(field)
		}
		return "✏️ <b>Изменено: " + strings.Join(labels, ", ") + "</b>\n\n◽️ <b>Было</b>\n" + formatLesson(*change.Old) + "\n\n▫️ <b>Стало</b>\n" + formatLesson(*change.New)
	}
	return ""
}

func fieldName(field changeModel.Field) string {
	switch field {
	case changeModel.FieldNumber:
		return "номер пары"
	case changeModel.FieldTime:
		return "время"
	case changeModel.FieldType:
		return "тип"
	case changeModel.FieldTeachers:
		return "преподаватель"
	case changeModel.FieldRooms:
		return "аудитория"
	}
	return string(field)
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
		return []string{fmt.Sprintf("⏳ <b>%s, %s</b>\n\nЗа этот день данных в кеше пока нет.", weekdayTitle(date), humanDate(date))}, nil
	}
	if err != nil {
		return nil, err
	}
	if week.Status == schedule.Unpublished {
		return []string{fmt.Sprintf("📭 <b>Расписание ещё не опубликовано</b>\n\nНеделя с %s\n🕘 Проверено: %s", humanDate(week.Monday), checked(week.CheckedAt, h.calendar.Location))}, nil
	}
	var lessons []schedule.Lesson
	for _, lesson := range week.Lessons {
		if sameDate(lesson.Date, date, h.calendar.Location) {
			lessons = append(lessons, lesson)
		}
	}
	weekType, _ := h.calendar.WeekTypeAt(date)
	header := fmt.Sprintf("📅 <b>%s, %s</b>\n%s", weekdayTitle(date), humanDate(date), weekBadge(weekType))
	if len(lessons) == 0 {
		return []string{header + "\n\n🌿 Занятий нет — можно выдохнуть."}, nil
	}
	return formatLessonChunks(header, lessons), nil
}

func (h *Handler) week(ctx context.Context, date time.Time) ([]string, error) {
	week, err := h.repo.GetWeek(ctx, h.groupID, date)
	if errors.Is(err, storage.ErrNotFound) {
		return []string{fmt.Sprintf("⏳ <b>Неделя с %s</b>\n\nДанных в кеше пока нет.", humanDate(schedule.Monday(date, h.calendar.Location)))}, nil
	}
	if err != nil {
		return nil, err
	}
	if week.Status == schedule.Unpublished {
		return []string{fmt.Sprintf("📭 <b>Расписание ещё не опубликовано</b>\n\nНеделя с %s\n🕘 Проверено: %s", humanDate(week.Monday), checked(week.CheckedAt, h.calendar.Location))}, nil
	}
	byDate := map[string][]schedule.Lesson{}
	for _, lesson := range week.Lessons {
		key := lesson.Date.In(h.calendar.Location).Format(time.DateOnly)
		byDate[key] = append(byDate[key], lesson)
	}
	result := []string{fmt.Sprintf("🗓 <b>%s — %s</b>\n%s", humanDate(week.Monday), humanDate(week.Monday.AddDate(0, 0, 6)), weekBadge(week.WeekType))}
	for day := 0; day < 7; day++ {
		date := week.Monday.AddDate(0, 0, day)
		lessons := byDate[date.Format(time.DateOnly)]
		if len(lessons) > 0 {
			result = append(result, formatLessonChunks(fmt.Sprintf("📅 <b>%s, %s</b>", weekdayTitle(date), humanDate(date)), lessons)...)
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
		header := fmt.Sprintf("⏭ <b>Ближайшая пара</b>\n📅 %s, %s", weekdayTitle(first), humanDate(first))
		messages := formatLessonChunks(header, simultaneous)
		if incomplete {
			messages[0] += "\n\n⚠️ <i>До этой пары есть недели без опубликованных данных. Результат может быть неполным.</i>"
		}
		return messages, nil
	}
	if incomplete {
		return []string{"🔎 <b>Ближайшая пара не найдена</b>\n\n⚠️ В кеше есть пробелы, поэтому поиск может быть неполным."}, nil
	}
	return []string{"🌿 <b>Занятий не найдено</b>\n\nВ ближайшие 12 недель расписание свободно."}, nil
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
	label := "Пара"
	if lesson.Number != nil {
		label = fmt.Sprintf("%d пара", *lesson.Number)
	}
	line := fmt.Sprintf("⏰ <b>%s · %s–%s</b>\n📚 %s", label, minute(lesson.StartMinute), minute(lesson.EndMinute), html.EscapeString(clip(lesson.Subject, 200)))
	meta := []string{"🎓 " + typeName(lesson)}
	if lesson.Subgroup != "" {
		meta = append(meta, "👥 Подгруппа "+html.EscapeString(clip(lesson.Subgroup, 40)))
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
		meta = append(meta, "👤 "+strings.Join(teachers, ", "))
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
		meta = append(meta, "📍 "+strings.Join(rooms, ", "))
	}
	return line + "\n" + strings.Join(meta, "\n")
}

func (h *Handler) help() string {
	return fmt.Sprintf("🌙 <b>Lunara · группа %d</b>\n\n📅 /today — расписание на сегодня\n🌤 /tomorrow — расписание на завтра\n🗓 /week — текущая неделя\n⏭ /nextweek — следующая неделя\n⏰ /next — ближайшая пара\n🔎 /day ДД.ММ.ГГГГ — выбранный день\n🔔 /changes — история изменений\n🎓 /exams — экзамены\n❔ /help — эта справка", h.groupID)
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
func weekdayTitle(value time.Time) string {
	return [...]string{"Воскресенье", "Понедельник", "Вторник", "Среда", "Четверг", "Пятница", "Суббота"}[value.Weekday()]
}
func humanDate(value time.Time) string {
	months := [...]string{"", "января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
	return fmt.Sprintf("%d %s %d", value.Day(), months[value.Month()], value.Year())
}
func weekBadge(value schedule.WeekType) string {
	if value == schedule.Upper {
		return "🔼 Верхняя неделя"
	}
	return "🔽 Нижняя неделя"
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
