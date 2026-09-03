// skyq — sky transparency analyser. Phase 1: the morning report.
//
// Subcommands:
//
//	skyq report    [--night 2026-09-02]   analyse one night, write + publish the report
//	skyq backfill  [--nights 30]          re-run report for recent nights with logs
//	skyq calibrate [--night 2026-09-02]   suggest a volatility threshold from a clear night
//	skyq serve                            Phase 2 live monitor (not yet implemented)
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	// Embed the IANA timezone database: the NUC has no Go install, so
	// without this "Australia/Melbourne" is an unknown zone there.
	_ "time/tzdata"

	"github.com/exploded/skyq/internal/config"
)

func main() {
	log.SetFlags(log.LstdFlags)
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgPath := fs.String("config", "config.json", "path to config file")
	night := fs.String("night", "", "night to analyse (evening date, YYYY-MM-DD; default: the night that just ended)")
	nights := fs.Int("nights", 30, "backfill: how many nights back to try")
	fs.Parse(os.Args[2:])

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	loc, err := cfg.Location()
	if err != nil {
		log.Fatalf("timezone: %v", err)
	}

	switch cmd {
	case "report":
		if err := runReport(cfg, loc, nightOf(*night, loc)); err != nil {
			log.Fatal(err)
		}
	case "backfill":
		ran := 0
		for i := 0; i < *nights; i++ {
			n := time.Now().In(loc).AddDate(0, 0, -1-i).Format("2006-01-02")
			err := runReport(cfg, loc, n)
			switch {
			case err == nil:
				ran++
			case os.IsNotExist(err) || err == errNoLogs:
				continue
			default:
				log.Printf("night %s: %v", n, err)
			}
		}
		log.Printf("backfill complete: %d nights", ran)
	case "calibrate":
		if err := runCalibrate(cfg, loc, nightOf(*night, loc)); err != nil {
			log.Fatal(err)
		}
	case "serve":
		log.Fatal("skyq serve is Phase 2 and not implemented yet")
	default:
		usage()
	}
}

// nightOf resolves the evening date: explicit flag, or "the night that just
// ended" — now minus 12 hours, so a 09:00 run reports yesterday evening.
func nightOf(flagVal string, loc *time.Location) string {
	if flagVal != "" {
		return flagVal
	}
	return time.Now().In(loc).Add(-12 * time.Hour).Format("2006-01-02")
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: skyq <report|backfill|calibrate|serve> [flags]")
	os.Exit(2)
}
