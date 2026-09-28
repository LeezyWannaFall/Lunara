package miigaik

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
)

func TestParseExams(t *testing.T) {
	cal := testCalendar(t)
	file, err := os.Open("testdata/exams.html")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := ParseExams(file, 1306, cal)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != schedule.Published || len(got.Exams) != 2 || got.Exams[0].Subject != "Высшая математика" || got.Exams[0].Kind != "Экзамен" || got.Hash == "" {
		t.Fatalf("unexpected exams: %+v", got)
	}
}

func TestParseExamsUnpublished(t *testing.T) {
	file, err := os.Open("testdata/exams_empty.html")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := ParseExams(file, 1306, testCalendar(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != schedule.Unpublished || len(got.Exams) != 0 {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestParseExamsRejectsBrokenPage(t *testing.T) {
	_, err := ParseExams(strings.NewReader(`<html><body><div id="exam-schedule-results"></div></body></html>`), 1306, testCalendar(t))
	if !errors.Is(err, ErrUnexpectedPage) {
		t.Fatalf("unexpected error: %v", err)
	}
}
