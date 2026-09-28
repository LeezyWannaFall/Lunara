package miigaik

import (
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/PuerkitoBio/goquery"
)

func testCalendar(t *testing.T) schedule.Calendar {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := schedule.ParseDate("2025-09-01", loc)
	if err != nil {
		t.Fatal(err)
	}
	return schedule.Calendar{Location: loc, AnchorMonday: anchor, AnchorType: schedule.Upper}
}
func testDate(t *testing.T, date string) time.Time {
	t.Helper()
	d, err := schedule.ParseDate(date, testCalendar(t).Location)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".html")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func parse(t *testing.T, html string) (schedule.Schedule, error) {
	t.Helper()
	return ParseWeek(strings.NewReader(html), 1306, testDate(t, "2026-09-28"), testCalendar(t))
}
func mutate(t *testing.T, html string, change func(*goquery.Document)) string {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatal(err)
	}
	change(doc)
	result, err := doc.Html()
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRealWeeks(t *testing.T) {
	for _, tc := range []struct {
		name, date string
		kind       schedule.WeekType
		count      int
		status     schedule.Status
	}{
		{"upper", "2026-09-28", schedule.Upper, 15, schedule.Published},
		{"lower", "2026-10-05", schedule.Lower, 13, schedule.Published},
		{"empty", "2027-08-02", schedule.Upper, 0, schedule.Unpublished},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseWeek(strings.NewReader(fixture(t, tc.name)), 1306, testDate(t, tc.date), testCalendar(t))
			if err != nil {
				t.Fatal(err)
			}
			if got.GroupID != 1306 || got.WeekType != tc.kind || got.Status != tc.status || len(got.Lessons) != tc.count || len(got.Hash) != 64 || !got.CheckedAt.IsZero() {
				t.Fatalf("bad result: %+v", got)
			}
			if tc.count == 0 {
				return
			}
			lesson := got.Lessons[0]
			if lesson.Subject != "Инженерное обустройство территорий" || lesson.Type != schedule.Lecture || lesson.RawType != "" || lesson.StartMinute != 870 || lesson.EndMinute != 960 || lesson.Number == nil || *lesson.Number != 4 {
				t.Fatalf("bad lesson: %+v", lesson)
			}
			if !reflect.DeepEqual(lesson.Teachers, []string{"Снежинская Елена Юрьевна"}) || !reflect.DeepEqual(lesson.Rooms, []schedule.Room{{Name: "53", Building: "Старый корпус"}}) {
				t.Fatalf("bad teacher/room: %+v", lesson)
			}
		})
	}
}

func TestRejectsBrokenPagesWithoutPartialResults(t *testing.T) {
	for name, change := range map[string]func(*goquery.Document){
		"missing root":  func(d *goquery.Document) { d.Find("#group-schedule-results").Remove() },
		"missing group": func(d *goquery.Document) { d.Find("#schedule-group-select-config").Remove() },
		"wrong group": func(d *goquery.Document) {
			s := d.Find("#schedule-group-select-config")
			s.SetText(strings.ReplaceAll(s.Text(), "1306", "9999"))
		},
		"wrong heading": func(d *goquery.Document) { d.Find("h2").SetText("Another group") },
		"wrong week":    func(d *goquery.Document) { d.Find(".weekday-block span").First().SetText("06.10.2026") },
		"wrong weekday": func(d *goquery.Document) {
			d.Find(".day-block").First().SetAttr("data-weekday", "Понедельник")
		},
		"missing time":    func(d *goquery.Document) { d.Find(".lesson-block").Last().RemoveAttr("data-lesson-start") },
		"bad time":        func(d *goquery.Document) { d.Find(".lesson-block").Last().SetAttr("data-lesson-end", "25:00") },
		"reversed time":   func(d *goquery.Document) { d.Find(".lesson-block").Last().SetAttr("data-lesson-end", "01:00") },
		"missing subject": func(d *goquery.Document) { d.Find(".lesson-left h4").Last().Remove() },
		"missing type":    func(d *goquery.Document) { d.Find(".lesson-left").Last().ChildrenFiltered("p").Remove() },
		"wrong number":    func(d *goquery.Document) { d.Find(".lesson-num").Last().SetText("abc") },
		"missing column":  func(d *goquery.Document) { d.Find(".lesson-right").Last().Remove() },
		"empty day":       func(d *goquery.Document) { d.Find(".day-block").Last().Find(".lesson-block").Remove() },
		"duplicate day": func(d *goquery.Document) {
			d.Find(".schedule-block").AppendSelection(d.Find(".day-block").First().Clone())
		},
		"mixed empty": func(d *goquery.Document) {
			d.Find(".schedule-block").AppendHtml(`<div class="day-block schedule-empty">Нет данных для отображения. Выберите группу или другую неделю.</div>`)
		},
		"unknown metadata": func(d *goquery.Document) { d.Find(".lesson-left").Last().AppendHtml("<p>Unexpected detail</p>") },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parse(t, mutate(t, fixture(t, "upper"), change))
			if !errors.Is(err, ErrUnexpectedPage) || !reflect.DeepEqual(got, schedule.Schedule{}) {
				t.Fatalf("got partial/successful result: %+v, %v", got, err)
			}
		})
	}
	for _, html := range []string{"<html><body>upstream error</body></html>", strings.TrimSuffix(fixture(t, "upper"), "</body></html>\n")} {
		if _, err := parse(t, html); !errors.Is(err, ErrUnexpectedPage) {
			t.Fatalf("accepted invalid/truncated HTML: %v", err)
		}
	}
}

func TestNormalizationAndOptionalFields(t *testing.T) {
	original := fixture(t, "upper")
	baseline, err := parse(t, original)
	if err != nil {
		t.Fatal(err)
	}
	reformatted := mutate(t, original, func(d *goquery.Document) {
		d.Find(".lesson-left h4").First().SetHtml("  Инженерное&nbsp; обустройство\n территорий  ")
		d.Find(".lesson-left").First().ChildrenFiltered("p").First().SetText("Лекция")
		day := d.Find(".day-block").Eq(1)
		card := day.ChildrenFiltered(".lesson-block").First().Remove()
		day.AppendSelection(card)
	})
	got, err := parse(t, reformatted)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hash != baseline.Hash {
		t.Fatal("formatting, ordering or type alias changes hash")
	}
	optional := mutate(t, original, func(d *goquery.Document) {
		left := d.Find(".lesson-left").First()
		left.ChildrenFiltered("p").First().SetText("Индивидуальная консультация")
		left.AppendHtml("<p>Подгруппа: 2</p>")
		left.Find(".lesson-num").Remove()
		right := d.Find(".lesson-right").First()
		right.ChildrenFiltered("span").Remove()
		right.Find(".aud-info-wrap").Remove()
	})
	got, err = parse(t, optional)
	if err != nil {
		t.Fatal(err)
	}
	lesson := got.Lessons[0]
	if lesson.Type != schedule.Other || lesson.RawType != "Индивидуальная консультация" || lesson.Subgroup != "2" || lesson.Number != nil || len(lesson.Rooms) != 0 || len(lesson.Teachers) != 0 {
		t.Fatalf("bad optional fields: %+v", lesson)
	}
	lab := mutate(t, original, func(d *goquery.Document) {
		d.Find(".lesson-left").First().ChildrenFiltered("p").First().SetText("Лабораторные работы")
		d.Find(".lesson-right").First().ChildrenFiltered("span").SetHtml("Яковлев Я. Я.<br>Андреев А. А.")
	})
	got, err = parse(t, lab)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lessons[0].Type != schedule.Lab || !reflect.DeepEqual(got.Lessons[0].Teachers, []string{"Андреев А. А.", "Яковлев Я. Я."}) {
		t.Fatalf("bad lab/teachers: %+v", got.Lessons[0])
	}
	duplicates := mutate(t, original, func(d *goquery.Document) {
		day := d.Find(".day-block").First()
		day.AppendSelection(day.Find(".lesson-block").First().Clone())
	})
	got, err = parse(t, duplicates)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lessons) != len(baseline.Lessons)+1 || got.Hash == baseline.Hash {
		t.Fatal("duplicate lesson lost")
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestParserLimitsAndInput(t *testing.T) {
	if _, err := parse(t, strings.Repeat("x", int(MaxResponseBytes)+1)); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("size limit: %v", err)
	}
	if _, err := ParseWeek(brokenReader{}, 1306, testDate(t, "2026-09-28"), testCalendar(t)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read error: %v", err)
	}
	for _, tc := range []struct {
		group int64
		date  string
	}{{0, "2026-09-28"}, {1306, "2026-09-29"}} {
		if _, err := ParseWeek(strings.NewReader(fixture(t, "upper")), tc.group, testDate(t, tc.date), testCalendar(t)); err == nil {
			t.Fatal("accepted invalid request")
		}
	}
	if _, err := ParseWeek(strings.NewReader(""), 1306, time.Time{}, schedule.Calendar{}); err == nil {
		t.Fatal("accepted invalid calendar")
	}
}

func TestContentChangesAffectHash(t *testing.T) {
	original := fixture(t, "upper")
	baseline, err := parse(t, original)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*goquery.Document){
		"time": func(d *goquery.Document) { d.Find(".lesson-block").First().SetAttr("data-lesson-start", "14:35") },
		"teacher": func(d *goquery.Document) {
			d.Find(".lesson-right").First().ChildrenFiltered("span").SetText("Другой преподаватель")
		},
		"room": func(d *goquery.Document) { d.Find(".aud-popup p").First().SetText("Аудитория: 99") },
		"type": func(d *goquery.Document) {
			d.Find(".lesson-left").First().ChildrenFiltered("p").First().SetText("Практические занятия")
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parse(t, mutate(t, original, change))
			if err != nil {
				t.Fatal(err)
			}
			if got.Hash == baseline.Hash {
				t.Fatal("content change lost")
			}
		})
	}
}
