package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/LeezyWannaFall/Lunara/internal/config"
)

func main() { os.Exit(run()) }

func run() int {
	bootstrap := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		bootstrap.Error("configuration rejected", "component", "config", "error", err)
		return 1
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})).With("component", "app", "group_id", cfg.GroupID)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	weekType, err := cfg.Calendar.WeekTypeAt(time.Now())
	if err != nil {
		logger.Error("calendar initialization failed", "error", err)
		return 1
	}
	logger.Info("application started", "stage", 1, "timezone", cfg.Calendar.Location.String(), "week_type", weekType)
	// Stage 1 has no workers or network clients yet.
	<-ctx.Done()
	stop() // A second signal may terminate immediately.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", "error", err)
		return 1
	}
	logger.Info("application stopped")
	return 0
}

// Later stages close their workers and clients here using the bounded context.
func shutdown(ctx context.Context) error { return ctx.Err() }
