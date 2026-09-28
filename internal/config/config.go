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
	GroupID              int64
	DatabaseURL          string
	BootstrapWeeks       int
	StartupTimeout       time.Duration
	Calendar             schedule.Calendar
	LogLevel             slog.Level
	ShutdownTimeout      time.Duration
	TelegramToken        string
	TelegramChatID       int64
	WatchWeeks           int
	WatchInterval        time.Duration
	ConfirmationDelay    time.Duration
	DeliveryPollInterval time.Duration
	ExamInterval         time.Duration
	ReminderPollInterval time.Duration
	TomorrowReminder     bool
	WeeklyReminder       bool
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
	c.DatabaseURL = strings.TrimSpace(getenv("DATABASE_URL"))
	c.TelegramToken = strings.TrimSpace(getenv("TELEGRAM_BOT_TOKEN"))
	var err error
	if raw := strings.TrimSpace(getenv("TELEGRAM_CHAT_ID")); raw != "" {
		c.TelegramChatID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || c.TelegramChatID == 0 {
			return c, fmt.Errorf("TELEGRAM_CHAT_ID must be a non-zero integer")
		}
	}
	c.BootstrapWeeks, err = strconv.Atoi(value("BOOTSTRAP_WEEKS", "2"))
	if err != nil || c.BootstrapWeeks < 1 || c.BootstrapWeeks > 12 {
		return c, fmt.Errorf("BOOTSTRAP_WEEKS must be between 1 and 12")
	}
	c.WatchWeeks, err = strconv.Atoi(value("WATCH_WEEKS", "2"))
	if err != nil || c.WatchWeeks < 1 || c.WatchWeeks > 12 {
		return c, fmt.Errorf("WATCH_WEEKS must be between 1 and 12")
	}
	c.WatchInterval, err = time.ParseDuration(value("WATCH_INTERVAL", "24h"))
	if err != nil || c.WatchInterval <= 0 {
		return c, fmt.Errorf("WATCH_INTERVAL must be a positive duration")
	}
	c.ConfirmationDelay, err = time.ParseDuration(value("CONFIRMATION_DELAY", "10m"))
	if err != nil || c.ConfirmationDelay < 10*time.Minute {
		return c, fmt.Errorf("CONFIRMATION_DELAY must be at least 10m")
	}
	c.DeliveryPollInterval, err = time.ParseDuration(value("DELIVERY_POLL_INTERVAL", "5s"))
	if err != nil || c.DeliveryPollInterval <= 0 {
		return c, fmt.Errorf("DELIVERY_POLL_INTERVAL must be a positive duration")
	}
	c.ExamInterval, err = time.ParseDuration(value("EXAM_INTERVAL", "24h"))
	if err != nil || c.ExamInterval <= 0 {
		return c, fmt.Errorf("EXAM_INTERVAL must be a positive duration")
	}
	c.ReminderPollInterval, err = time.ParseDuration(value("REMINDER_POLL_INTERVAL", "1m"))
	if err != nil || c.ReminderPollInterval <= 0 {
		return c, fmt.Errorf("REMINDER_POLL_INTERVAL must be a positive duration")
	}
	c.TomorrowReminder, err = strconv.ParseBool(value("REMINDER_TOMORROW_ENABLED", "false"))
	if err != nil {
		return c, fmt.Errorf("REMINDER_TOMORROW_ENABLED must be true or false")
	}
	c.WeeklyReminder, err = strconv.ParseBool(value("REMINDER_WEEKLY_ENABLED", "false"))
	if err != nil {
		return c, fmt.Errorf("REMINDER_WEEKLY_ENABLED must be true or false")
	}
	c.StartupTimeout, err = time.ParseDuration(value("STARTUP_TIMEOUT", "2m"))
	if err != nil || c.StartupTimeout <= 0 {
		return c, fmt.Errorf("STARTUP_TIMEOUT must be a positive duration")
	}
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
