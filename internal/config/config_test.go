package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	env := map[string]string{}
	c, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.GroupID != 1306 || c.GroupName != "2023-ФУТ-УпрК-1б" || c.TelegramThreadID != 0 || c.Calendar.Location.String() != "Europe/Moscow" || c.ShutdownTimeout != 20*time.Second || c.BootstrapWeeks != 2 || c.StartupTimeout != 2*time.Minute || c.WatchWeeks != 2 || c.WatchInterval != 24*time.Hour || c.ConfirmationDelay != 10*time.Minute || c.DeliveryPollInterval != 5*time.Second || c.ExamInterval != 24*time.Hour || c.ReminderPollInterval != time.Minute || c.TomorrowReminder || c.WeeklyReminder {
		t.Fatalf("wrong defaults: %+v", c)
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"WEEK_ANCHOR", "2026-02-30"}, {"GROUP_ID", "0"}, {"GROUP_ID", "abc"},
		{"GROUP_NAME", strings.Repeat("я", 101)},
		{"TIMEZONE", "Missing/Zone"}, {"WEEK_ANCHOR", "2025-09-02"},
		{"WEEK_ANCHOR_TYPE", "odd"}, {"LOG_LEVEL", "verbose"},
		{"BOOTSTRAP_WEEKS", "0"}, {"BOOTSTRAP_WEEKS", "13"}, {"BOOTSTRAP_WEEKS", "abc"},
		{"STARTUP_TIMEOUT", "0s"}, {"STARTUP_TIMEOUT", "bad"},
		{"SHUTDOWN_TIMEOUT", "0s"}, {"SHUTDOWN_TIMEOUT", "bad"},
		{"TELEGRAM_CHAT_ID", "abc"}, {"TELEGRAM_CHAT_ID", "0"},
		{"TELEGRAM_THREAD_ID", "abc"}, {"TELEGRAM_THREAD_ID", "0"}, {"TELEGRAM_THREAD_ID", "42"},
		{"WATCH_WEEKS", "0"}, {"WATCH_WEEKS", "13"}, {"WATCH_INTERVAL", "0s"},
		{"CONFIRMATION_DELAY", "9m"}, {"DELIVERY_POLL_INTERVAL", "0s"},
		{"EXAM_INTERVAL", "0s"}, {"REMINDER_POLL_INTERVAL", "bad"}, {"REMINDER_TOMORROW_ENABLED", "maybe"}, {"REMINDER_WEEKLY_ENABLED", "yes-please"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			env := map[string]string{}
			env[tc.key] = tc.value
			if _, err := Load(func(k string) string { return env[k] }); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
