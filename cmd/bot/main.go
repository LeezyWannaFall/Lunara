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
	cancelStartup()
	if err != nil && ctx.Err() == nil {
		var sourceErr *scheduler.SourceError
		if !errors.As(err, &sourceErr) {
			store.Close()
			logger.Error("initial schedule load failed", "error", err)
			return 1
		}
		logger.Warn("source unavailable; cached schedules retained", "cached_weeks", report.Cached, "error", err)
	}
	if ctx.Err() == nil {
		logger.Info("application started", "stage", 3, "timezone", cfg.Calendar.Location.String(), "cached_weeks", report.Cached, "inserted_weeks", report.Inserted)
		<-ctx.Done()
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
