package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/LeezyWannaFall/Lunara/internal/config"
	"github.com/LeezyWannaFall/Lunara/internal/scheduler"
	"github.com/LeezyWannaFall/Lunara/internal/source/miigaik"
	"github.com/LeezyWannaFall/Lunara/internal/storage"
	telegrambot "github.com/LeezyWannaFall/Lunara/internal/telegram"
)

func main() { os.Exit(run()) }

func run() int {
	bootstrapLogger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		bootstrapLogger.Error("configuration rejected", "component", "config", "error", err)
		return 1
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})).With("component", "app", "group_id", cfg.GroupID)
	if cfg.TelegramToken == "" {
		logger.Error("TELEGRAM_BOT_TOKEN is required")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startupCtx, cancelStartup := context.WithTimeout(ctx, cfg.StartupTimeout)
	defer cancelStartup()
	store, err := storage.Open(startupCtx, cfg.DatabaseURL, cfg.Calendar)
	if err != nil {
		if ctx.Err() != nil {
			return 0
		}
		logger.Error("database initialization failed", "error", err)
		return 1
	}
	source, err := miigaik.NewClient(cfg.Calendar, nil, "")
	if err != nil {
		store.Close()
		logger.Error("source initialization failed", "error", err)
		return 1
	}
	report, err := scheduler.Bootstrap(startupCtx, source, store, cfg.Calendar, cfg.GroupID, time.Now(), cfg.BootstrapWeeks)
	if err != nil && ctx.Err() == nil {
		var sourceErr *scheduler.SourceError
		if !errors.As(err, &sourceErr) {
			store.Close()
			logger.Error("initial schedule load failed", "error", err)
			return 1
		}
		logger.Warn("source unavailable; cached schedules retained", "cached_weeks", report.Cached, "error", err)
	}
	handler, err := telegrambot.NewHandler(store, cfg.Calendar, cfg.GroupID)
	if err != nil {
		cancelStartup()
		store.Close()
		logger.Error("Telegram handler initialization failed", "error", err)
		return 1
	}
	telegramAPI, err := telegrambot.NewClient(cfg.TelegramToken, nil, "")
	if err != nil {
		cancelStartup()
		store.Close()
		logger.Error("Telegram client initialization failed", "error", err)
		return 1
	}
	bot, err := telegrambot.NewBot(startupCtx, telegramAPI, handler, store, cfg.TelegramChatID, logger.With("component", "telegram"))
	cancelStartup()
	if err != nil {
		store.Close()
		logger.Error("Telegram initialization failed", "error", err)
		return 1
	}
	if ctx.Err() == nil {
		logger.Info("application started", "stage", 4, "timezone", cfg.Calendar.Location.String(), "cached_weeks", report.Cached, "inserted_weeks", report.Inserted)
		if err := bot.Run(ctx); err != nil && ctx.Err() == nil {
			store.Close()
			logger.Error("Telegram polling stopped", "error", err)
			return 1
		}
	}
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := shutdown(shutdownCtx, store.Close); err != nil {
		logger.Error("shutdown failed", "error", err)
		return 1
	}
	logger.Info("application stopped")
	return 0
}

func shutdown(ctx context.Context, closeDB func()) error {
	done := make(chan struct{})
	go func() { closeDB(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
