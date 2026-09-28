package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	env := map[string]string{}
	c, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.GroupID != 1306 || c.Calendar.Location.String() != "Europe/Moscow" || c.ShutdownTimeout != 20*time.Second {
		t.Fatalf("wrong defaults: %+v", c)
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"WEEK_ANCHOR", "2026-02-30"}, {"GROUP_ID", "0"}, {"GROUP_ID", "abc"},
		{"TIMEZONE", "Missing/Zone"}, {"WEEK_ANCHOR", "2025-09-02"},
		{"WEEK_ANCHOR_TYPE", "odd"}, {"LOG_LEVEL", "verbose"},
		{"SHUTDOWN_TIMEOUT", "0s"}, {"SHUTDOWN_TIMEOUT", "bad"},
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
