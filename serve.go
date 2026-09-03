package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/exploded/skyq/internal/allsky"
	"github.com/exploded/skyq/internal/alpaca"
	"github.com/exploded/skyq/internal/analysis"
	"github.com/exploded/skyq/internal/config"
	"github.com/exploded/skyq/internal/live"
	"github.com/exploded/skyq/internal/server"
	"github.com/exploded/skyq/internal/store"
	db "github.com/exploded/skyq/internal/store/db"
)

// runServe is Phase 2: the live monitor. Long-running; Task Scheduler
// at-startup entry with restart-on-failure.
func runServe(cfg *config.Config, loc *time.Location) error {
	if cfg.Latitude == 0 && cfg.Longitude == 0 {
		return errors.New("set latitude and longitude in config.json — the darkness gate needs the site location")
	}

	var fetcher *allsky.Fetcher
	if cfg.AllSkyBaseURL != "" {
		fetcher = &allsky.Fetcher{
			BaseURL: cfg.AllSkyBaseURL, Username: cfg.AllSkyUsername,
			Password: cfg.AllSkyPassword, CacheDir: cfg.CacheDir,
		}
	}

	// Baselines are read-only here; the 09:00 report job is the writer.
	// WAL + busy_timeout make the concurrent access safe.
	sdb, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer sdb.Close()
	queries := db.New(sdb)
	baselines := func() map[analysis.BaselineKey]analysis.Baseline {
		rows, err := queries.ListBaselines(context.Background())
		if err != nil {
			log.Printf("serve: loading baselines: %v", err)
			return nil
		}
		out := make(map[analysis.BaselineKey]analysis.Baseline, len(rows))
		for _, r := range rows {
			out[analysis.BaselineKey{Target: r.Target, Filter: r.Filter}] =
				analysis.Baseline{MedianStars: r.MedianStars, NFrames: int(r.NFrames)}
		}
		return out
	}

	engine := live.New(live.Options{
		Fetcher: fetcher, CacheDir: cfg.CacheDir, Loc: loc,
		Latitude: cfg.Latitude, Longitude: cfg.Longitude,
		Window: cfg.VolatilityWindow, Threshold: cfg.VolatilityThreshold,
		BandTop: cfg.BandTop, BandBottom: cfg.BandBottom,
		MaxStillAge: time.Duration(cfg.MaxStillAgeMin) * time.Minute,
		Poll:        time.Duration(cfg.PollSeconds) * time.Second,
		NINALogDir:  cfg.NINALogDir,
		Baselines:   baselines,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go engine.Run(ctx)

	alpacaSrv := &http.Server{Addr: cfg.AlpacaAddr, Handler: alpaca.New(engine).Handler()}
	liveSrv := &http.Server{Addr: cfg.LiveAddr, Handler: server.New(engine, loc).Handler()}

	fail := make(chan error, 2)
	go func() {
		log.Printf("Alpaca ObservingConditions on http://%s/api/v1/observingconditions/0/ (no discovery — add manually in N.I.N.A.)", cfg.AlpacaAddr)
		if err := alpacaSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fail <- fmt.Errorf("alpaca listener: %w", err)
		}
	}()
	go func() {
		log.Printf("live page on http://localhost%s/", cfg.LiveAddr)
		if err := liveSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fail <- fmt.Errorf("live page listener: %w", err)
		}
	}()

	select {
	case err := <-fail:
		stop()
		return err
	case <-ctx.Done():
	}
	log.Println("shutting down")
	shctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	alpacaSrv.Shutdown(shctx)
	liveSrv.Shutdown(shctx)
	return nil
}
