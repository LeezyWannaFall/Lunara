// Command admin performs explicit operator-only maintenance actions.
package main

import (
	"context"
	"fmt"
	"os"
	"time"
	_ "time/tzdata"

	"github.com/LeezyWannaFall/Lunara/internal/config"
	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/LeezyWannaFall/Lunara/internal/storage"
)

func main() { os.Exit(run()) }

func run() int {
	if len(os.Args) < 2 || (os.Args[1] != "quarantines" && os.Args[1] != "accept-quarantine") {
		fmt.Fprintln(os.Stderr, "usage: admin quarantines | admin accept-quarantine YYYY-MM-DD")
		return 1
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	store, err := storage.Open(ctx, cfg.DatabaseURL, cfg.Calendar)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer store.Close()
	switch os.Args[1] {
	case "quarantines":
		if len(os.Args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: admin quarantines")
			return 1
		}
		items, err := store.Quarantines(ctx, cfg.GroupID)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if len(items) == 0 {
			fmt.Println("no quarantined schedules")
			return 0
		}
		for _, item := range items {
			fmt.Printf("%s\t%s\n", item.Monday.Format(time.DateOnly), item.Reason)
		}
	case "accept-quarantine":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: admin accept-quarantine YYYY-MM-DD")
			return 1
		}
		date, err := schedule.ParseDate(os.Args[2], cfg.Calendar.Location)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		set, err := store.AcceptQuarantineForChat(ctx, cfg.GroupID, date, time.Now(), cfg.TelegramChatID)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("accepted change set %d for week %s\n", set.ID, set.WeekStarts[0].Format(time.DateOnly))
	}
	return 0
}
