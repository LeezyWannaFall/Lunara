package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/storage"
	"github.com/LeezyWannaFall/Lunara/migrations"
)

func main() { os.Exit(run()) }
func run() int {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("component", "migrations")
	if len(os.Args) != 2 || (os.Args[1] != "up" && os.Args[1] != "down" && os.Args[1] != "status") {
		logger.Error("usage: migrate up|down|status")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	db, err := storage.OpenSQL(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		logger.Error("database connection failed", "error", err)
		return 1
	}
	defer db.Close()
	provider, err := migrations.New(db)
	if err != nil {
		logger.Error("migration initialization failed", "error", err)
		return 1
	}
	switch os.Args[1] {
	case "up":
		_, err = provider.Up(ctx)
	case "down":
		_, err = provider.Down(ctx)
	case "status":
		var current, target int64
		current, target, err = provider.GetVersions(ctx)
		if err == nil {
			logger.Info("migration status", "current", current, "target", target)
			return 0
		}
	}
	if err != nil {
		logger.Error("migration failed", "error", err)
		return 1
	}
	logger.Info("migration completed", "action", os.Args[1])
	return 0
}
