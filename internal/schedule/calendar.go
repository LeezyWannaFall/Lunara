package schedule

import (
	"fmt"
	"time"
)

func ParseDate(value string, location *time.Location) (time.Time, error) {
	if location == nil {
		return time.Time{}, fmt.Errorf("date location is required")
	}
	date, err := time.ParseInLocation(time.DateOnly, value, location)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date %q: %w", value, err)
	}
	return date, nil
}

func LocalDate(value time.Time, location *time.Location) time.Time {
	local := value.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
}

func Monday(value time.Time, location *time.Location) time.Time {
	date := LocalDate(value, location)
	return date.AddDate(0, 0, -(int(date.Weekday())+6)%7)
}

// civilDay uses UTC only to count calendar days, avoiding DST-dependent durations.
func civilDay(date time.Time) int64 {
	return time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC).Unix() / 86400
}

func (s Calendar) Validate() error {
	if s.Location == nil {
		return fmt.Errorf("calendar location is required")
	}
	if s.AnchorMonday.IsZero() {
		return fmt.Errorf("week anchor is required")
	}
	if !s.AnchorMonday.Equal(LocalDate(s.AnchorMonday, s.Location)) {
		return fmt.Errorf("week anchor must be local midnight")
	}
	if s.AnchorMonday.In(s.Location).Weekday() != time.Monday {
		return fmt.Errorf("week anchor must be a Monday")
	}
	if s.AnchorType != Upper && s.AnchorType != Lower {
		return fmt.Errorf("week anchor type must be upper or lower")
	}
	return nil
}

func (s Calendar) WeekTypeAt(value time.Time) (WeekType, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	weeks := (civilDay(Monday(value, s.Location)) - civilDay(s.AnchorMonday.In(s.Location))) / 7
	if weeks%2 == 0 {
		return s.AnchorType, nil
	}
	if s.AnchorType == Upper {
		return Lower, nil
	}
	return Upper, nil
}
