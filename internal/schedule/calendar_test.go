package schedule

import (
	"testing"
	"time"
	_ "time/tzdata"
)

func calendar(t *testing.T, zone string) Calendar {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	parse := func(s string) time.Time {
		d, e := ParseDate(s, loc)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	return Calendar{Location: loc, AnchorMonday: parse("2025-09-01"), AnchorType: Upper}
}

func TestWeekType(t *testing.T) {
	s := calendar(t, "Europe/Moscow")
	for _, tc := range []struct {
		date string
		want WeekType
	}{
		{"2025-09-01", Upper}, {"2025-09-07", Upper}, {"2025-09-08", Lower},
		{"2025-08-31", Lower}, {"2025-08-24", Upper},
		{"2025-12-29", Lower}, {"2026-01-01", Lower}, {"2026-01-05", Upper},
		{"2026-09-28", Upper},
	} {
		t.Run(tc.date, func(t *testing.T) {
			d, err := ParseDate(tc.date, s.Location)
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.WeekTypeAt(d)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	s.AnchorType = Lower
	got, err := s.WeekTypeAt(s.AnchorMonday.AddDate(0, 0, 7))
	if err != nil || got != Upper {
		t.Fatalf("lower anchor: %s, %v", got, err)
	}
}

func TestMoscowMidnight(t *testing.T) {
	s := calendar(t, "Europe/Moscow")
	instant, _ := time.Parse(time.RFC3339, "2025-09-07T21:00:00Z")
	got, err := s.WeekTypeAt(instant)
	if err != nil || got != Lower {
		t.Fatalf("Moscow Monday: %s %v", got, err)
	}

}

func TestCalendarAcrossDST(t *testing.T) {
	s := calendar(t, "Europe/Berlin")
	s.AnchorMonday, _ = ParseDate("2025-03-24", s.Location)
	d, _ := ParseDate("2025-03-31", s.Location)
	got, err := s.WeekTypeAt(d)
	if err != nil || got != Lower {
		t.Fatalf("DST: %s, %v", got, err)
	}
}

func TestInvalidDatesAndCalendar(t *testing.T) {
	s := calendar(t, "Europe/Moscow")
	for _, date := range []string{"", "2025-02-29", "2026-13-01", "28.09.2026"} {
		if _, err := ParseDate(date, s.Location); err == nil {
			t.Errorf("accepted %q", date)
		}
	}
	if _, err := ParseDate("2026-09-28", nil); err == nil {
		t.Fatal("accepted nil location")
	}
	s.AnchorMonday = s.AnchorMonday.AddDate(0, 0, 1)
	if _, err := s.WeekTypeAt(time.Now()); err == nil {
		t.Fatal("accepted Tuesday anchor")
	}
}
