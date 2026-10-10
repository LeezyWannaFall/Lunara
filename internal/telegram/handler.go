package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"slices"
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
	repo      ScheduleRepository
	exams     ExamRepository
	calendar  schedule.Calendar
	groupID   int64
	groupName string
	now       func() time.Time
}

func NewHandler(repo ScheduleRepository, calendar schedule.Calendar, groupID int64, names ...string) (*Handler, error) {
	if repo == nil || groupID <= 0 {
		return nil, fmt.Errorf("schedule repository and positive group ID are required")
	}
	if err := calendar.Validate(); err != nil {
		return nil, err
	}
	exams, _ := repo.(ExamRepository)
	groupName := fmt.Sprintf("Группа %d", groupID)
	if len(names) > 0 && strings.TrimSpace(names[0]) != "" {
		groupName = strings.TrimSpace(names[0])
	}
	return &Handler{repo: repo, exams: exams, calendar: calendar, groupID: groupID, groupName: groupName, now: time.Now}, nil
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
	header := fmt.Sprintf("🎓 <b>Экзамены · %s</b>\n🕘 Проверено: %s", html.EscapeString(h.groupName), checked(value.CheckedAt, h.calendar.Location))
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
	line := fmt.Sprintf("📅 <b>%s, %s</b>\n⏰ <b>%s</b>\n📚 %s", weekdayTitle(exam.Date), humanDate(exam.Date), minute(exam.StartMinute), html.EscapeString(exam.Subject))
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
			rooms = append(rooms, formatRoom(room))
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
	header := "🔔 <b>Изменения в расписании</b>"
	if set.Kind == changeModel.MajorChange {
		header += "\n\n" + majorChangeNotice(set)
	}
	footer := fmt.Sprintf("<i>Обнаружено: %s</i>", set.DetectedAt.In(loc).Format("02.01.2006 · 15:04"))
	if set.Kind == changeModel.FirstPublication {
		dates := make([]string, len(set.WeekStarts))
		for i, week := range set.WeekStarts {
			dates[i] = week.Format("02.01.2006")
		}
		return []string{header + "\n\n✅ Опубликовано расписание на недели:\n" + strings.Join(dates, ", ") + "\n\n" + footer}
	}
	items := presentChanges(set.Changes)
	if len(items) > 0 {
		header += fmt.Sprintf("\n%s · %s", changeCountLabel(len(items)), changeDateRange(items))
	}
	result := []string{}
	current := header
	for _, item := range items {
		block := formatPresentedChange(item)
		if item.count > 1 {
			block += fmt.Sprintf("\n<i>Одинаковых записей: %d</i>", item.count)
		}
		if len([]rune(block)) > 3800 {
			block = compactPresentedChange(item)
		}
		if len([]rune(current))+len([]rune(block))+len([]rune(footer))+4 > 4096 {
			result = append(result, current+"\n\n"+footer)
			current = header + " (продолжение)\n\n" + block
		} else {
			current += "\n\n" + block
		}
	}
	return append(result, current+"\n\n"+footer)
}

func majorChangeNotice(set changeModel.ChangeSet) string {
	counts := map[changeModel.Kind]int{}
	for _, change := range set.Changes {
		counts[change.Kind]++
	}
	return fmt.Sprintf("⚠️ <b>Расписание сильно изменилось</b>\n\nУдалено занятий: %d\nДобавлено занятий: %d\n\n<i>Изменения подтверждены повторной проверкой сайта.</i>", counts[changeModel.Removed], counts[changeModel.Added])
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
	header := "🔔 <b>Изменения в расписании</b>"
	if set.Kind == changeModel.MajorChange {
		header += "\n\n" + majorChangeNotice(set)
	}
	return fmt.Sprintf("%s\n\n➕ Добавлено: %d\n✏️ Изменено: %d\n➖ Убрано: %d\n\nПодробности: /changes\n\n<i>Обнаружено: %s</i>", header,
		counts[changeModel.Added], counts[changeModel.Modified], counts[changeModel.Removed], set.DetectedAt.In(loc).Format("02.01.2006 · 15:04"))
}

type presentedChange struct {
	change *changeModel.Change
	old    *schedule.Lesson
	new    *schedule.Lesson
	moved  bool
	count  int
}

func presentChanges(values []changeModel.Change) []presentedChange {
	groups := groupIdenticalChanges(values)
	used := make([]bool, len(groups))
	result := make([]presentedChange, 0, len(groups))
	for i, group := range groups {
		if used[i] {
			continue
		}
		if (group.change.Kind == changeModel.Removed || group.change.Kind == changeModel.Added) && !group.change.Ambiguous {
			wanted := changeModel.Added
			if group.change.Kind == changeModel.Added {
				wanted = changeModel.Removed
			}
			matches := moveMatches(groups, used, i, wanted)
			if len(matches) == 1 && len(moveMatches(groups, used, matches[0], group.change.Kind)) == 1 && group.count == groups[matches[0]].count {
				other := groups[matches[0]]
				oldLesson, newLesson := group.change.Old, other.change.New
				if group.change.Kind == changeModel.Added {
					oldLesson, newLesson = other.change.Old, group.change.New
				}
				used[i], used[matches[0]] = true, true
				result = append(result, presentedChange{old: oldLesson, new: newLesson, moved: true, count: group.count})
				continue
			}
		}
		used[i] = true
		value := group.change
		result = append(result, presentedChange{change: &value, old: value.Old, new: value.New, count: group.count})
	}
	return result
}

func moveMatches(groups []identicalChangeGroup, used []bool, source int, wanted changeModel.Kind) []int {
	var sourceLesson *schedule.Lesson
	if groups[source].change.Kind == changeModel.Removed {
		sourceLesson = groups[source].change.Old
	} else {
		sourceLesson = groups[source].change.New
	}
	result := []int{}
	for i, group := range groups {
		if i == source || used[i] || group.change.Kind != wanted || group.change.Ambiguous {
			continue
		}
		var candidate *schedule.Lesson
		if wanted == changeModel.Removed {
			candidate = group.change.Old
		} else {
			candidate = group.change.New
		}
		if moveIdentity(sourceLesson, candidate) {
			result = append(result, i)
		}
	}
	return result
}

func moveIdentity(a, b *schedule.Lesson) bool {
	return a != nil && b != nil && a.Subject == b.Subject && a.Subgroup == b.Subgroup && slices.Equal(a.Teachers, b.Teachers) && (!a.Date.Equal(b.Date) || a.StartMinute != b.StartMinute || !numberEqual(a.Number, b.Number))
}

func formatPresentedChange(item presentedChange) string {
	if item.moved {
		return formatMovedChange(*item.old, *item.new)
	}
	return formatChange(*item.change)
}

func compactPresentedChange(item presentedChange) string {
	lesson := item.new
	mark := "🔄"
	label := "перенесено"
	if !item.moved {
		lesson = item.change.New
		if lesson == nil {
			lesson = item.change.Old
		}
		mark = map[changeModel.Kind]string{changeModel.Added: "➕", changeModel.Removed: "➖", changeModel.Modified: "✏️"}[item.change.Kind]
		label = string(item.change.Kind)
	}
	return fmt.Sprintf("%s <b>%s</b> · %s · %s", mark, label, lesson.Date.Format("02.01.2006"), html.EscapeString(lesson.Subject))
}

func formatChange(change changeModel.Change) string {
	mark := ""
	if change.Ambiguous {
		mark = "\n\n⚠️ <i>Связь между занятиями неоднозначна.</i>"
	}
	switch change.Kind {
	case changeModel.Added:
		return "➕ <b>Добавлено занятие</b>\n📚 " + html.EscapeString(change.New.Subject) + "\n📅 " + shortDate(change.New.Date) + "\n⏰ " + lessonSlot(*change.New) + "\n\n" + moreDetails(formatLessonMetadata(*change.New)) + mark
	case changeModel.Removed:
		return "➖ <b>Убрано из расписания</b>\n📚 " + html.EscapeString(change.Old.Subject) + "\n📅 " + shortDate(change.Old.Date) + "\n⏰ " + lessonSlot(*change.Old) + "\n\n" + moreDetails(formatLessonMetadata(*change.Old)) + mark
	case changeModel.Modified:
		title := "✏️ <b>Изменено занятие</b>"
		if len(change.Fields) == 1 && change.Fields[0] == changeModel.FieldRooms {
			title = "🏫 <b>Изменилась аудитория</b>"
		}
		summary := make([]string, 0, len(change.Fields))
		if containsField(change.Fields, changeModel.FieldNumber) || containsField(change.Fields, changeModel.FieldTime) {
			summary = append(summary, "⏰ "+lessonSlot(*change.Old)+" → "+lessonSlot(*change.New))
		}
		for _, field := range change.Fields {
			if field == changeModel.FieldNumber || field == changeModel.FieldTime {
				continue
			}
			summary = append(summary, formatFieldTransition(field, *change.Old, *change.New))
		}
		result := title + "\n📚 " + html.EscapeString(change.New.Subject) + "\n📅 " + shortDate(change.New.Date)
		if len(change.Fields) == 1 && change.Fields[0] == changeModel.FieldRooms {
			result += " · " + lessonSlot(*change.New)
		}
		result += "\n" + strings.Join(summary, "\n")
		if len(change.Fields) > 1 {
			if details := unchangedDetails(*change.New, change.Fields); details != "" {
				result += "\n\n" + moreDetails(details)
			}
		}
		return result + mark
	}
	return ""
}

func formatMovedChange(oldLesson, newLesson schedule.Lesson) string {
	result := "🔄 <b>Перенесено занятие</b>\n📚 " + html.EscapeString(newLesson.Subject)
	result += "\n📅 " + shortDate(oldLesson.Date) + " → " + shortDate(newLesson.Date)
	result += "\n⏰ " + lessonSlot(oldLesson) + " → " + lessonSlot(newLesson)
	if oldLesson.Type != newLesson.Type || oldLesson.RawType != newLesson.RawType {
		result += "\n🎓 " + typeName(oldLesson) + " → " + typeName(newLesson)
	}
	if !slices.Equal(oldLesson.Rooms, newLesson.Rooms) {
		result += "\n📍 " + formatRoomsPlain(oldLesson.Rooms) + " → " + formatRooms(newLesson.Rooms)
	}
	return result + "\n\n" + moreDetails(formatLessonMetadata(newLesson))
}

type identicalChangeGroup struct {
	change changeModel.Change
	count  int
}

func groupIdenticalChanges(values []changeModel.Change) []identicalChangeGroup {
	groups := make([]identicalChangeGroup, 0, len(values))
	for _, value := range values {
		matched := false
		for i := range groups {
			if changesEqual(groups[i].change, value) {
				groups[i].count++
				matched = true
				break
			}
		}
		if !matched {
			groups = append(groups, identicalChangeGroup{change: value, count: 1})
		}
	}
	return groups
}

func changesEqual(a, b changeModel.Change) bool {
	return a.Kind == b.Kind && a.Ambiguous == b.Ambiguous && slices.Equal(a.Fields, b.Fields) && lessonsEqual(a.Old, b.Old) && lessonsEqual(a.New, b.New)
}

func lessonsEqual(a, b *schedule.Lesson) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Date.Equal(b.Date) && numberEqual(a.Number, b.Number) && a.StartMinute == b.StartMinute && a.EndMinute == b.EndMinute && a.Subject == b.Subject && a.Type == b.Type && a.RawType == b.RawType && a.Subgroup == b.Subgroup && slices.Equal(a.Teachers, b.Teachers) && slices.Equal(a.Rooms, b.Rooms)
}

func numberEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func containsField(fields []changeModel.Field, wanted changeModel.Field) bool {
	return slices.Contains(fields, wanted)
}

func lessonSlot(lesson schedule.Lesson) string {
	return fmt.Sprintf("%s · %s–%s", lessonNumber(lesson), minute(lesson.StartMinute), minute(lesson.EndMinute))
}

func shortDate(date time.Time) string {
	weekdays := [...]string{"Вс", "Пн", "Вт", "Ср", "Чт", "Пт", "Сб"}
	return fmt.Sprintf("%s, %s", weekdays[date.Weekday()], humanDateWithoutYear(date))
}

func formatLessonMetadata(lesson schedule.Lesson) string {
	values := []string{"🎓 " + typeName(lesson)}
	if lesson.Subgroup != "" {
		values = append(values, "👥 "+html.EscapeString(lesson.Subgroup))
	}
	if len(lesson.Teachers) > 0 {
		values = append(values, "👤 "+formatTeachers(lesson.Teachers))
	}
	if len(lesson.Rooms) > 0 {
		values = append(values, "📍 "+formatRooms(lesson.Rooms))
	}
	return strings.Join(values, "\n")
}

func unchangedDetails(lesson schedule.Lesson, changed []changeModel.Field) string {
	values := []string{}
	if !containsField(changed, changeModel.FieldType) {
		values = append(values, "🎓 "+typeName(lesson))
	}
	if lesson.Subgroup != "" {
		values = append(values, "👥 "+html.EscapeString(lesson.Subgroup))
	}
	if len(lesson.Teachers) > 0 && !containsField(changed, changeModel.FieldTeachers) {
		values = append(values, "👤 "+formatTeachers(lesson.Teachers))
	}
	if len(lesson.Rooms) > 0 && !containsField(changed, changeModel.FieldRooms) {
		values = append(values, "📍 "+formatRooms(lesson.Rooms))
	}
	return strings.Join(values, "\n")
}

func moreDetails(value string) string {
	return "<i>Подробнее</i>\n" + expandableQuote(value)
}

func changeCountLabel(count int) string {
	word := "изменений"
	if count%10 == 1 && count%100 != 11 {
		word = "изменение"
	} else if count%10 >= 2 && count%10 <= 4 && (count%100 < 12 || count%100 > 14) {
		word = "изменения"
	}
	return fmt.Sprintf("%d %s", count, word)
}

func changeDateRange(items []presentedChange) string {
	var first, last time.Time
	for _, item := range items {
		for _, date := range []time.Time{lessonDate(item.old), lessonDate(item.new)} {
			if date.IsZero() {
				continue
			}
			if first.IsZero() || date.Before(first) {
				first = date
			}
			if last.IsZero() || date.After(last) {
				last = date
			}
		}
	}
	if sameCalendarDate(first, last) {
		return humanDateWithoutYear(first)
	}
	if first.Year() == last.Year() && first.Month() == last.Month() {
		return fmt.Sprintf("%d–%d %s", first.Day(), last.Day(), monthName(last.Month()))
	}
	return humanDateWithoutYear(first) + " — " + humanDateWithoutYear(last)
}

func lessonDate(lesson *schedule.Lesson) time.Time {
	if lesson == nil {
		return time.Time{}
	}
	return lesson.Date
}

func sameCalendarDate(a, b time.Time) bool {
	return !a.IsZero() && a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

func formatFieldTransition(field changeModel.Field, oldLesson, newLesson schedule.Lesson) string {
	switch field {
	case changeModel.FieldNumber:
		return "⏰ Пара: " + lessonNumber(oldLesson) + " → " + lessonNumber(newLesson)
	case changeModel.FieldTime:
		return fmt.Sprintf("⏰ Время: %s–%s → %s–%s", minute(oldLesson.StartMinute), minute(oldLesson.EndMinute), minute(newLesson.StartMinute), minute(newLesson.EndMinute))
	case changeModel.FieldType:
		return "🎓 " + typeName(oldLesson) + " → " + typeName(newLesson)
	case changeModel.FieldTeachers:
		return "👤 " + formatTeachers(oldLesson.Teachers) + " → " + formatTeachers(newLesson.Teachers)
	case changeModel.FieldRooms:
		return "📍 " + formatRoomsPlain(oldLesson.Rooms) + " → " + formatRooms(newLesson.Rooms)
	default:
		return html.EscapeString(string(field))
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
	return formatExpandableLessonChunks(header, lessons), nil
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
	header := fmt.Sprintf("🗓 <b>%s</b>\n%s", weekRange(week.Monday), weekBadge(week.WeekType))
	result := []string{}
	current := header
	for day := 0; day < 7; day++ {
		date := week.Monday.AddDate(0, 0, day)
		lessons := byDate[date.Format(time.DateOnly)]
		if len(lessons) > 0 {
			body := make([]string, len(lessons))
			for i, lesson := range lessons {
				body[i] = formatWeekLesson(lesson)
			}
			block := fmt.Sprintf("📅 <b>%s, %s</b>\n%s", weekdayTitle(date), humanDateWithoutYear(date), expandableQuote(strings.Join(body, "\n\n")))
			candidate := current + "\n\n" + block
			if len([]rune(candidate)) > 4096 {
				result = append(result, current)
				current = header + " (продолжение)\n\n" + block
			} else {
				current = candidate
			}
		}
	}
	return append(result, current), nil
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

func formatExpandableLessonChunks(header string, lessons []schedule.Lesson) []string {
	chunks := []string{}
	body := []string{}
	for i := range lessons {
		var previous *schedule.Lesson
		if i > 0 {
			previous = &lessons[i-1]
		}
		block := formatLessonCompared(lessons[i], previous)
		candidateBody := append(append([]string{}, body...), block)
		candidate := header + "\n\n" + expandableQuote(strings.Join(candidateBody, "\n\n"))
		if len([]rune(candidate)) > 4096 && len(body) > 0 {
			chunks = append(chunks, header+"\n\n"+expandableQuote(strings.Join(body, "\n\n")))
			header += " (продолжение)"
			body = []string{formatLessonCompared(lessons[i], nil)}
			continue
		}
		body = candidateBody
	}
	return append(chunks, header+"\n\n"+expandableQuote(strings.Join(body, "\n\n")))
}

func formatWeekLesson(lesson schedule.Lesson) string {
	return fmt.Sprintf("⏰ <b>%s · %s–%s</b>\n📚 %s", lessonNumber(lesson), minute(lesson.StartMinute), minute(lesson.EndMinute), html.EscapeString(lesson.Subject))
}

func formatLesson(lesson schedule.Lesson) string {
	return formatLessonCompared(lesson, nil)
}

func formatLessonCompared(lesson schedule.Lesson, previous *schedule.Lesson) string {
	line := fmt.Sprintf("⏰ <b>%s · %s–%s · %s</b>\n📚 %s", lessonNumber(lesson), minute(lesson.StartMinute), minute(lesson.EndMinute), typeName(lesson), html.EscapeString(lesson.Subject))
	meta := []string{}
	if lesson.Subgroup != "" {
		meta = append(meta, "👥 Подгруппа "+html.EscapeString(clip(lesson.Subgroup, 40)))
	}
	sameTeachers := previous != nil && len(lesson.Teachers) > 0 && slices.Equal(lesson.Teachers, previous.Teachers)
	sameRooms := previous != nil && len(lesson.Rooms) > 0 && slices.Equal(lesson.Rooms, previous.Rooms)
	if sameTeachers && sameRooms {
		meta = append(meta, "↳ преподаватель и аудитория те же")
	} else {
		if len(lesson.Teachers) > 0 {
			if sameTeachers {
				meta = append(meta, "👤 тот же преподаватель")
			} else {
				meta = append(meta, "👤 "+formatTeachers(lesson.Teachers))
			}
		}
		if len(lesson.Rooms) > 0 {
			if sameRooms {
				meta = append(meta, "📍 та же аудитория")
			} else {
				meta = append(meta, "📍 "+formatRooms(lesson.Rooms))
			}
		}
	}
	if len(meta) == 0 {
		return line
	}
	return line + "\n" + strings.Join(meta, "\n")
}

func (h *Handler) help() string {
	return fmt.Sprintf("🌙 <b>Lunara</b>\n🎓 %s\n\n📅 /today — расписание на сегодня\n🌤 /tomorrow — расписание на завтра\n🗓 /week — текущая неделя\n⏭ /nextweek — следующая неделя\n⏰ /next — ближайшая пара\n🔎 /day ДД.ММ.ГГГГ — выбранный день\n🔔 /changes — история изменений\n🎓 /exams — экзамены\n❔ /help — эта справка", html.EscapeString(h.groupName))
}

func formatRoom(room schedule.Room) string {
	name := html.EscapeString(clip(room.Name, 60))
	if room.Building != "" {
		name += " (" + html.EscapeString(clip(room.Building, 60)) + ")"
	}
	parsed, err := url.Parse(room.MapURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "map.miigaik.ru" || parsed.User != nil {
		return name
	}
	return name + ` · <a href="` + html.EscapeString(parsed.String()) + `">Карта</a>`
}

func formatTeachers(teachers []string) string {
	if len(teachers) == 0 {
		return "не указан"
	}
	values := make([]string, len(teachers))
	for i, teacher := range teachers {
		values[i] = html.EscapeString(teacher)
	}
	return strings.Join(values, ", ")
}

func formatRooms(rooms []schedule.Room) string {
	if len(rooms) == 0 {
		return "не указана"
	}
	values := make([]string, len(rooms))
	for i, room := range rooms {
		values[i] = formatRoom(room)
	}
	return strings.Join(values, ", ")
}

func formatRoomsPlain(rooms []schedule.Room) string {
	if len(rooms) == 0 {
		return "не указана"
	}
	values := make([]string, len(rooms))
	for i, room := range rooms {
		value := html.EscapeString(room.Name)
		if room.Building != "" {
			value += " (" + html.EscapeString(room.Building) + ")"
		}
		values[i] = value
	}
	return strings.Join(values, ", ")
}

func lessonNumber(lesson schedule.Lesson) string {
	if lesson.Number == nil {
		return "Пара"
	}
	return fmt.Sprintf("%d пара", *lesson.Number)
}

func expandableQuote(value string) string {
	return "<blockquote expandable>" + value + "</blockquote>"
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
	return fmt.Sprintf("%d %s %d", value.Day(), monthName(value.Month()), value.Year())
}
func humanDateWithoutYear(value time.Time) string {
	return fmt.Sprintf("%d %s", value.Day(), monthName(value.Month()))
}
func monthName(value time.Month) string {
	return [...]string{"", "января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}[value]
}
func weekRange(monday time.Time) string {
	sunday := monday.AddDate(0, 0, 6)
	if monday.Year() == sunday.Year() && monday.Month() == sunday.Month() {
		return fmt.Sprintf("%d–%d %s %d", monday.Day(), sunday.Day(), monthName(sunday.Month()), sunday.Year())
	}
	if monday.Year() == sunday.Year() {
		return fmt.Sprintf("%s — %s %d", humanDateWithoutYear(monday), humanDateWithoutYear(sunday), sunday.Year())
	}
	return humanDate(monday) + " — " + humanDate(sunday)
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
