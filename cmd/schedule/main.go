// Command schedule fetches one week as JSON for manual source verification.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/LeezyWannaFall/Lunara/internal/config"
	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/LeezyWannaFall/Lunara/internal/source/miigaik"
)

func main() { os.Exit(run()) }
func run() int {
	dateFlag := flag.String("date", "", "any date within the desired week (YYYY-MM-DD); defaults to today")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("component", "schedule-check")
	if flag.NArg() != 0 {
		logger.Error("unexpected positional arguments")
		return 1
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		logger.Error("configuration rejected", "error", err)
		return 1
	}
	date := time.Now()
	if *dateFlag != "" {
		date, err = schedule.ParseDate(*dateFlag, cfg.Calendar.Location)
		if err != nil {
			logger.Error("invalid date", "error", err)
			return 1
		}
	}
	client, err := miigaik.NewClient(cfg.Calendar, nil, "")
	if err != nil {
		logger.Error("client initialization failed", "error", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	result, err := client.FetchWeek(ctx, cfg.GroupID, date)
	if err != nil {
		logger.Error("schedule fetch failed", "group_id", cfg.GroupID, "error", err)
		return 1
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		logger.Error("write result failed", "error", err)
		return 1
	}
	return 0
}
