package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/LeezyWannaFall/Lunara/internal/changes"
	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/LeezyWannaFall/Lunara/internal/storage"
)

type fakeRepo struct {
	weeks        map[string]schedule.Schedule
	history      []changes.ChangeSet
	examSchedule *schedule.ExamSchedule
}

func (f fakeRepo) GetExams(context.Context, int64) (schedule.ExamSchedule, error) {
	if f.examSchedule == nil {
		return schedule.ExamSchedule{}, storage.ErrNotFound
	}
	return *f.examSchedule, nil
}

func (f fakeRepo) GetWeek(_ context.Context, _ int64, date time.Time) (schedule.Schedule, error) {
	week, ok := f.weeks[schedule.Monday(date, date.Location()).Format(time.DateOnly)]
	if !ok {
		return schedule.Schedule{}, storage.ErrNotFound
	}
	return week, nil
}
func (f fakeRepo) ChangeSets(_ context.Context, _ int64, before int64, limit int) ([]changes.ChangeSet, error) {
	var result []changes.ChangeSet
	for _, set := range f.history {
		if before == 0 || set.ID < before {
			result = append(result, set)
		}
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func testHandler(t *testing.T, weeks map[string]schedule.Schedule) *Handler {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	anchor, _ := schedule.ParseDate("2025-09-01", loc)
	h, err := NewHandler(fakeRepo{weeks: weeks}, schedule.Calendar{Location: loc, AnchorMonday: anchor, AnchorType: schedule.Upper}, 1306, "2023-ФУТ-УпрК-1б")
	if err != nil {
		t.Fatal(err)
	}
	h.now = func() time.Time { return time.Date(2026, 9, 28, 10, 0, 0, 0, loc) }
	return h
}

func published(t *testing.T) schedule.Schedule {
	t.Helper()
	loc, _ := time.LoadLocation("Europe/Moscow")
	monday, _ := schedule.ParseDate("2026-09-28", loc)
	n := 2
	return schedule.Schedule{GroupID: 1306, Monday: monday, WeekType: schedule.Upper, Status: schedule.Published, CheckedAt: time.Now(), Lessons: []schedule.Lesson{
		{Date: monday, Number: &n, StartMinute: 600, EndMinute: 690, Subject: "Math <advanced>", Type: schedule.Lecture, Teachers: []string{"A & B"}, Rooms: []schedule.Room{{Name: "101", Building: "A", MapURL: "https://map.miigaik.ru/#id=101"}}},
		{Date: monday.AddDate(0, 0, 1), StartMinute: 540, EndMinute: 630, Subject: "Physics", Type: schedule.Lab},
	}}
}

func TestCommandsAndHTMLFormatting(t *testing.T) {
	w := published(t)
	h := testHandler(t, map[string]schedule.Schedule{"2026-09-28": w})
	for _, command := range []string{"/start", "/help", "/today", "/tomorrow", "/week", "/day 28.09.2026", "/day 2026-09-28", "/next", "/changes", "/exams"} {
		messages, err := h.Handle(context.Background(), command, "LunaraBot")
		if err != nil || len(messages) == 0 {
			t.Fatalf("%s: messages=%v err=%v", command, messages, err)
		}
		for _, message := range messages {
			if len([]rune(message)) > 4096 {
				t.Fatalf("%s produced oversized message", command)
			}
		}
	}
	messages, _ := h.Handle(context.Background(), "/today@lunarabot", "LunaraBot")
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "📅 <b>Понедельник, 28 сентября 2026</b>") || !strings.Contains(joined, "<blockquote expandable>") || !strings.Contains(joined, "⏰ <b>2 пара · 10:00–11:30 · лекция</b>") || !strings.Contains(joined, "📚 Math &lt;advanced&gt;") || !strings.Contains(joined, "👤 A &amp; B") || !strings.Contains(joined, `📍 101 (A) · <a href="https://map.miigaik.ru/#id=101">Карта</a>`) {
		t.Fatalf("HTML not escaped: %s", joined)
	}
	help, _ := h.Handle(context.Background(), "/help", "bot")
	if !strings.Contains(help[0], "2023-ФУТ-УпрК-1б") {
		t.Fatalf("group name missing: %s", help[0])
	}
	if messages, _ := h.Handle(context.Background(), "/today@OtherBot", "LunaraBot"); messages != nil {
		t.Fatal("command for another bot accepted")
	}
}

func TestDayStatesAndArguments(t *testing.T) {
	h := testHandler(t, nil)
	checks := map[string]string{
		"/day": "Укажите дату", "/day nonsense": "Не удалось", "/today": "данных в кеше пока нет", "/unknown": "Неизвестная",
	}
	for command, want := range checks {
		messages, err := h.Handle(context.Background(), command, "bot")
		if err != nil || len(messages) != 1 || !strings.Contains(messages[0], want) {
			t.Fatalf("%s: %v %v", command, messages, err)
		}
	}
	if messages, _ := h.Handle(context.Background(), "hello", "bot"); messages != nil {
		t.Fatal("plain text handled as command")
	}
}

type errorRepo struct{}

func (errorRepo) GetWeek(context.Context, int64, time.Time) (schedule.Schedule, error) {
	return schedule.Schedule{}, errors.New("db")
}
func (errorRepo) ChangeSets(context.Context, int64, int64, int) ([]changes.ChangeSet, error) {
	return nil, errors.New("db")
}

func TestRepositoryErrorsAreReturned(t *testing.T) {
	h := testHandler(t, nil)
	h.repo = errorRepo{}
	if _, err := h.Handle(context.Background(), "/week", "bot"); err == nil {
		t.Fatal("repository error hidden")
	}
}

func TestExamsPublishedAndUnpublished(t *testing.T) {
	h := testHandler(t, nil)
	loc := h.calendar.Location
	published := schedule.ExamSchedule{GroupID: 1306, Status: schedule.Published, CheckedAt: time.Now(), Exams: []schedule.Exam{{Date: time.Date(2026, 1, 12, 0, 0, 0, 0, loc), StartMinute: 600, Subject: "Math <exam>", Kind: "Экзамен", Teachers: []string{"A & B"}}}}
	h.exams = fakeRepo{examSchedule: &published}
	messages, err := h.Handle(context.Background(), "/exams", "bot")
	if err != nil || !strings.Contains(messages[0], "Math &lt;exam&gt;") || !strings.Contains(messages[0], "A &amp; B") {
		t.Fatalf("unexpected published exams: %v %v", messages, err)
	}
	unpublished := schedule.ExamSchedule{GroupID: 1306, Status: schedule.Unpublished, CheckedAt: time.Now(), Exams: []schedule.Exam{}}
	h.exams = fakeRepo{examSchedule: &unpublished}
	messages, err = h.Handle(context.Background(), "/exams", "bot")
	if err != nil || !strings.Contains(messages[0], "пока не опубликовано") {
		t.Fatalf("unexpected unpublished exams: %v %v", messages, err)
	}
}

func TestChangesHistoryFormattingAndCursor(t *testing.T) {
	h := testHandler(t, nil)
	oldLesson := published(t).Lessons[0]
	newLesson := oldLesson
	newLesson.Rooms = []schedule.Room{{Name: "202"}}
	h.repo = fakeRepo{history: []changes.ChangeSet{
		{ID: 2, GroupID: 1306, DetectedAt: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC), WeekStarts: []time.Time{oldLesson.Date}, Kind: changes.Regular, Changes: []changes.Change{{Kind: changes.Modified, Old: &oldLesson, New: &newLesson, Fields: []changes.Field{changes.FieldRooms}}}},
		{ID: 1, GroupID: 1306, DetectedAt: time.Now(), WeekStarts: []time.Time{oldLesson.Date}, Kind: changes.FirstPublication},
	}}
	messages, next, err := h.Changes(context.Background(), "")
	if err != nil || next != 2 || len(messages) != 1 || !strings.Contains(messages[0], "1 изменение · 28 сентября") || !strings.Contains(messages[0], "Изменилась аудитория") || !strings.Contains(messages[0], "📅 Пн, 28 сентября · 2 пара · 10:00–11:30") || !strings.Contains(messages[0], "📍 101 (A) → 202") || strings.Contains(messages[0], "<blockquote expandable>") || !strings.Contains(messages[0], "<i>Обнаружено:") {
		t.Fatalf("messages=%v next=%d err=%v", messages, next, err)
	}
	messages, next, err = h.Changes(context.Background(), "2")
	if err != nil || next != 0 || !strings.Contains(messages[0], "Опубликовано расписание") {
		t.Fatalf("older=%v next=%d err=%v", messages, next, err)
	}
}

func TestAddedAndRemovedChangesIncludeLessonDates(t *testing.T) {
	oldLesson := published(t).Lessons[0]
	oldLesson.Date = time.Date(2026, time.October, 1, 0, 0, 0, 0, oldLesson.Date.Location())
	newLesson := oldLesson
	newLesson.Date = time.Date(2026, time.October, 3, 0, 0, 0, 0, oldLesson.Date.Location())

	removed := formatChange(changes.Change{Kind: changes.Removed, Old: &oldLesson})
	if !strings.Contains(removed, "📅 Чт, 1 октября") || !strings.Contains(removed, "<i>Подробнее</i>") || !strings.Contains(removed, "<blockquote expandable>") {
		t.Fatalf("removed change does not contain its date: %s", removed)
	}

	added := formatChange(changes.Change{Kind: changes.Added, New: &newLesson})
	if !strings.Contains(added, "📅 Сб, 3 октября") || !strings.Contains(added, "<i>Подробнее</i>") || !strings.Contains(added, "<blockquote expandable>") {
		t.Fatalf("added change does not contain its date: %s", added)
	}
}

func TestWeekUsesOneCompactExpandableMessage(t *testing.T) {
	week := published(t)
	h := testHandler(t, map[string]schedule.Schedule{"2026-09-28": week})
	messages, err := h.Handle(context.Background(), "/week", "bot")
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages=%v err=%v", messages, err)
	}
	message := messages[0]
	if !strings.Contains(message, "28 сентября — 4 октября 2026") || !strings.Contains(message, "Понедельник, 28 сентября") || !strings.Contains(message, "<blockquote expandable>") {
		t.Fatalf("week header or expandable days missing: %s", message)
	}
	if !strings.Contains(message, "Math &lt;advanced&gt;") || strings.Contains(message, "A &amp; B") || strings.Contains(message, "101 (A)") || strings.Contains(message, "лекция") {
		t.Fatalf("week is not compact or lost the full subject: %s", message)
	}
}

func TestDayCompactsRepeatedTeacherAndRoom(t *testing.T) {
	week := published(t)
	second := week.Lessons[0]
	n := 3
	second.Number = &n
	second.StartMinute = 750
	second.EndMinute = 840
	second.Subject = "Another full subject"
	week.Lessons = append(week.Lessons, second)
	h := testHandler(t, map[string]schedule.Schedule{"2026-09-28": week})
	messages, err := h.Handle(context.Background(), "/today", "bot")
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages=%v err=%v", messages, err)
	}
	if strings.Count(messages[0], "A &amp; B") != 1 || strings.Count(messages[0], "101 (A)") != 1 || !strings.Contains(messages[0], "преподаватель и аудитория те же") {
		t.Fatalf("repeated details were not compacted: %s", messages[0])
	}
}

func TestModifiedChangeShowsDateSummaryAndExpandableDetails(t *testing.T) {
	oldLesson := published(t).Lessons[0]
	newLesson := oldLesson
	newLesson.Type = schedule.Practice
	newLesson.Rooms = []schedule.Room{{Name: "414", Building: "Главный корпус"}}
	message := formatChange(changes.Change{Kind: changes.Modified, Old: &oldLesson, New: &newLesson, Fields: []changes.Field{changes.FieldType, changes.FieldRooms}})
	for _, want := range []string{"📚 Math &lt;advanced&gt;", "📅 Пн, 28 сентября", "🎓 лекция → практика", "📍 101 (A) → 414 (Главный корпус)", "<i>Подробнее</i>", "<blockquote expandable>👤 A &amp; B</blockquote>"} {
		if !strings.Contains(message, want) {
			t.Fatalf("%q missing from modified change: %s", want, message)
		}
	}
}

func TestRemovedAndAddedLessonBecomeOneMove(t *testing.T) {
	oldLesson := published(t).Lessons[0]
	newLesson := oldLesson
	newLesson.Date = oldLesson.Date.AddDate(0, 0, 1)
	n := 5
	newLesson.Number = &n
	newLesson.StartMinute, newLesson.EndMinute = 970, 1060
	set := changes.ChangeSet{DetectedAt: time.Now(), Kind: changes.Regular, Changes: []changes.Change{
		{Kind: changes.Removed, Old: &oldLesson},
		{Kind: changes.Added, New: &newLesson},
	}}
	messages := formatChangeSet(set, oldLesson.Date.Location())
	message := strings.Join(messages, "\n")
	for _, want := range []string{"1 изменение · 28–29 сентября", "Перенесено занятие", "Пн, 28 сентября → Вт, 29 сентября", "2 пара · 10:00–11:30 → 5 пара · 16:10–17:40", "<i>Подробнее</i>"} {
		if !strings.Contains(message, want) {
			t.Fatalf("%q missing from move: %s", want, message)
		}
	}
	if strings.Contains(message, "Убрано из расписания") || strings.Contains(message, "Добавлено занятие") {
		t.Fatalf("move was shown as separate changes: %s", message)
	}
}

func TestAmbiguousMoveCandidatesRemainSeparate(t *testing.T) {
	oldLesson := published(t).Lessons[0]
	newA, newB := oldLesson, oldLesson
	newA.Date = oldLesson.Date.AddDate(0, 0, 1)
	newB.Date = oldLesson.Date.AddDate(0, 0, 2)
	set := changes.ChangeSet{DetectedAt: time.Now(), Kind: changes.Regular, Changes: []changes.Change{
		{Kind: changes.Removed, Old: &oldLesson},
		{Kind: changes.Added, New: &newA},
		{Kind: changes.Added, New: &newB},
	}}
	message := strings.Join(formatChangeSet(set, oldLesson.Date.Location()), "\n")
	if strings.Contains(message, "Перенесено занятие") || !strings.Contains(message, "Убрано из расписания") || strings.Count(message, "Добавлено занятие") != 2 {
		t.Fatalf("ambiguous move was paired: %s", message)
	}
}

func TestIdenticalChangesAreGrouped(t *testing.T) {
	lesson := published(t).Lessons[0]
	change := changes.Change{Kind: changes.Removed, Old: &lesson}
	set := changes.ChangeSet{DetectedAt: time.Now(), Kind: changes.Regular, Changes: []changes.Change{change, change}}
	messages := formatChangeSet(set, lesson.Date.Location())
	if len(messages) != 1 || strings.Count(messages[0], "Убрано из расписания") != 1 || !strings.Contains(messages[0], "Одинаковых записей: 2") {
		t.Fatalf("identical changes were not grouped: %v", messages)
	}
}

func TestMajorChangeNotificationIsClearlyMarked(t *testing.T) {
	lesson := published(t).Lessons[0]
	set := changes.ChangeSet{
		DetectedAt: time.Date(2026, time.October, 6, 1, 44, 0, 0, time.UTC),
		Kind:       changes.MajorChange,
		Changes: []changes.Change{
			{Kind: changes.Removed, Old: &lesson},
			{Kind: changes.Added, New: &lesson},
		},
	}
	message := NotificationText(set, lesson.Date.Location())
	for _, want := range []string{"Расписание сильно изменилось", "Удалено занятий: 1", "Добавлено занятий: 1", "подтверждены повторной проверкой сайта"} {
		if !strings.Contains(message, want) {
			t.Fatalf("%q missing from major change notification: %s", want, message)
		}
	}
}
