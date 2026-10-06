package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/masapico/minihub/internal/config"
	"github.com/masapico/minihub/internal/storage"
	"github.com/masapico/minihub/internal/storage/backend"
)

func retention(args []string) error {
	flags := flag.NewFlagSet("retention", flag.ContinueOnError)
	configPath := flags.String("config", config.DefaultFilename, "configuration file")
	data := flags.String("data", "data", "persistent data directory")
	before := flags.String("before", "", "delete posts before YYYY-MM-DD in Asia/Tokyo")
	policy := flags.Bool("policy", false, "use retention.days from configuration")
	apply := flags.Bool("apply", false, "perform deletion (default: report only)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *before == "" && !*policy || *before != "" && *policy {
		return errors.New("usage: minihub retention [-config file] [-data directory] (-before YYYY-MM-DD | -policy) [-apply]")
	}
	explicit := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	cfg, loaded, err := config.Load(*configPath, explicit["config"])
	if err != nil {
		return err
	}
	if loaded && !explicit["data"] {
		*data = cfg.Server.DataDir
	}
	zone, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		return err
	}
	var cutoff time.Time
	if *policy {
		if cfg.Retention.Days < 1 {
			return errors.New("retention.days must be positive when -policy is used")
		}
		now := time.Now().In(zone)
		cutoff = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, zone).AddDate(0, 0, -cfg.Retention.Days)
	} else {
		cutoff, err = time.ParseInLocation("2006-01-02", *before, zone)
		if err != nil || cutoff.Format("2006-01-02") != *before {
			return fmt.Errorf("invalid -before date %q", *before)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := backend.Prune(ctx, *data, cfg.Storage.Type, cutoff, *apply)
	if err != nil {
		return fmt.Errorf("retention cutoff %s: %w", cutoff.Format("2006-01-02"), err)
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		storage.RetentionReport
		Applied bool `json:"applied"`
	}{report, *apply})
}
