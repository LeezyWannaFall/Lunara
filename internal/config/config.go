package config

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
)

type Config struct {
	GroupID         int64
	Calendar        schedule.Calendar
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
}

// Load accepts an environment reader to keep configuration tests isolated.
func Load(getenv func(string) string) (Config, error) {
	var c Config
	value := func(key, fallback string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return fallback
	}
	var err error
	c.GroupID, err = strconv.ParseInt(value("GROUP_ID", "1306"), 10, 64)
	if err != nil || c.GroupID <= 0 {
		return c, fmt.Errorf("GROUP_ID must be a positive integer")
	}
	c.Calendar.Location, err = time.LoadLocation(value("TIMEZONE", "Europe/Moscow"))
	if err != nil {
		return c, fmt.Errorf("TIMEZONE: %w", err)
	}
	c.Calendar.AnchorMonday, err = schedule.ParseDate(value("WEEK_ANCHOR", "2025-09-01"), c.Calendar.Location)
	if err != nil {
		return c, fmt.Errorf("WEEK_ANCHOR: %w", err)
	}
	c.Calendar.AnchorType = schedule.WeekType(value("WEEK_ANCHOR_TYPE", "upper"))
	if err := c.Calendar.Validate(); err != nil {
		return c, err
	}
	switch value("LOG_LEVEL", "info") {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "info":
		c.LogLevel = slog.LevelInfo
	case "warn":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		return c, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error")
	}
	c.ShutdownTimeout, err = time.ParseDuration(value("SHUTDOWN_TIMEOUT", "20s"))
	if err != nil || c.ShutdownTimeout <= 0 {
		return c, fmt.Errorf("SHUTDOWN_TIMEOUT must be a positive duration")
	}
	return c, nil
}
