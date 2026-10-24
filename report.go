package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/exploded/skyq/internal/analysis"
	"github.com/exploded/skyq/internal/config"
	"github.com/exploded/skyq/internal/ninalog"
	"github.com/exploded/skyq/internal/phd2"
	"github.com/exploded/skyq/internal/publish"
	"github.com/exploded/skyq/internal/report"
	"github.com/exploded/skyq/internal/skycam"
	"github.com/exploded/skyq/internal/store"
	db "github.com/exploded/skyq/internal/store/db"
)

var errNoLogs = errors.New("no N.I.N.A. logs for that night")

// errUnchanged is runReport declining a night whose log is already
// ingested unchanged, when asked to skip those.
var errUnchanged = errors.New("log unchanged since the night was ingested")

// minClearNightsForHistorical is when a (target, filter) baseline switches
// from the night's own pre-cloud median to a rolling median across nights.
const minClearNightsForHistorical = 5

// runReport analyses one night and writes, then publishes, its report.
// With skipUnchanged it does nothing for a night already ingested from the
// same log, and returns errUnchanged.
func runReport(cfg *config.Config, loc *time.Location, night string, skipUnchanged bool) error {
	nightStart, err := time.ParseInLocation("2006-01-02", night, loc)
	if err != nil {
		return fmt.Errorf("bad night %q: %w", night, err)
	}
	windowStart := nightStart.Add(12 * time.Hour) // local noon
	windowEnd := windowStart.Add(24 * time.Hour)

	logs, err := ninalog.FindNightLogs(cfg.NINALogDir, windowStart, windowEnd, loc)
	if err != nil {
		return err
	}
	if len(logs) == 0 {
		return errNoLogs
	}
	res, sha, err := ninalog.ParseFiles(logs, loc)
	if err != nil {
		return err
	}
	if len(res.Frames) == 0 {
		log.Printf("night %s: logs contain no frames (daytime session?) — skipping", night)
		return errNoLogs
	}
	log.Printf("night %s: %d logs, %d frames, %d events", night, len(logs), len(res.Frames), len(res.Events))
	if skipUnchanged {
		if same, err := ingestedFrom(cfg.DBPath, night, sha); err != nil {
			return err
		} else if same {
			log.Printf("night %s: log unchanged since it was ingested — skipping (--rerun forces it)", night)
			return errUnchanged
		}
	}

	// The imaging session, which skycam's cloud has to cover for the
	// night's cloud to count as known. Dawn flats can run past the last light
	// frame; the chart covers them too.
	sessionStart, sessionEnd := res.Frames[0].At, res.Frames[len(res.Frames)-1].At.Add(30*time.Minute)
	if n := len(res.Flats); n > 0 {
		if end := res.Flats[n-1].At.Add(10 * time.Minute); end.After(sessionEnd) {
			sessionEnd = end
		}
	}

	// Cloud comes from skycam on the Pi. A night it cannot serve, or one
	// with no cloud reading across the session, has unknown cloud: no
	// onset, nothing attributed to cloud. Unknown is never clear (SPEC §8).
	client := skycamClient(cfg)
	sky := skyNight(client, loc, night)
	cloud := cloudVerdict{Source: analysis.CloudUnknown}
	var skyFrames []skycam.Frame
	if sky != nil {
		for _, f := range sky.Frames {
			if !f.At.Before(sessionStart) && !f.At.After(sessionEnd) {
				skyFrames = append(skyFrames, f)
			}
		}
		if sky.CloudKnown(sessionStart, sessionEnd) {
			cloud = cloudVerdict{Onset: sky.Onset, HasOnset: sky.HasOnset, Source: analysis.CloudSkycam, KnownAt: sky.CloudKnownAt}
		} else {
			log.Printf("skycam has %d frames for %s but no cloud reading during the session — cloud unknown", len(sky.Frames), night)
		}
	}

	sdb, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer sdb.Close()
	q := db.New(sdb)
	ctx := context.Background()

	// A night already on record keeps its cloud verdict when skycam has
	// nothing for it now: every night before skycam (its onset came from the
	// retired luminance detector), or one rerun while the Pi is down. A rerun
	// must never quietly turn a known onset into unknown. The per-frame
	// readings are not stored, so a carried-forward onset covers the whole
	// night after it.
	if cloud.Source == analysis.CloudUnknown {
		if prev, err := q.GetNight(ctx, night); err == nil && analysis.CloudSource(prev.CloudSource) != analysis.CloudUnknown {
			cloud = cloudVerdict{Source: analysis.CloudSource(prev.CloudSource)}
			if prev.CloudOnsetAt.Valid {
				if t, err := time.Parse(time.RFC3339, prev.CloudOnsetAt.String); err == nil {
					cloud.Onset, cloud.HasOnset = t.In(loc), true
				}
			}
			log.Printf("no skycam cloud for %s; keeping the stored %s verdict", night, cloud.Source)
		}
	}
	switch {
	case cloud.HasOnset:
		log.Printf("cloud onset %s (%s)", cloud.Onset.Format("15:04"), cloud.Source)
	case cloud.Source == analysis.CloudUnknown:
		log.Printf("cloud unknown for %s — nothing will be attributed to cloud", night)
	}
	onset, hasOnset := cloud.Onset, cloud.HasOnset

	// Roof bands for the chart: skycam's roof comes from the roll-off
	// controller and sees every move. N.I.N.A.'s log only records the moves
	// N.I.N.A. made, so it is the fallback for a night skycam cannot serve.
	var roofSpans []analysis.RoofSpan
	roofFromLog := sky == nil
	if sky != nil {
		roofSpans = sky.RoofSpans()
	} else {
		roofSpans = analysis.NewRoofTimeline(res.RoofMoves, windowStart, windowEnd).Spans()
	}

	baselines, mode := chooseBaselines(ctx, q, res.Frames, onset, hasOnset, night)
	log.Printf("baseline mode: %s (%d pairs)", mode, len(baselines))

	events := analysis.Attribute(res.Events, res.Slews, res.TargetChanges, onset, hasOnset, cloud.KnownAt)
	var untrust []ninalog.AFRun
	for _, r := range res.AFRuns {
		if analysis.UntrustworthyAF(r, res.Frames) {
			untrust = append(untrust, r)
		}
	}

	if err := ingest(ctx, sdb, q, ingestInput{
		night: night, logPath: strings.Join(logs, ";"), sha: sha,
		res: res, events: events,
		onset: onset, hasOnset: hasOnset, source: cloud.Source,
		baselines: baselines, mode: mode,
	}); err != nil {
		return fmt.Errorf("ingest: %w", err)
	}

	// PHD2 guiding, over the same window. It answers a different question
	// from the sky — whether the mount tracked — so a missing guide log
	// costs the report one card and nothing else.
	guide := guideNight(cfg.PHD2LogDir, loc, windowStart, windowEnd)
	guideBins := analysis.GuideBins(guide.Samples, analysis.DefaultGuideBin)
	guideSummary := analysis.SummariseGuiding(guide, guideBins, onset, hasOnset)
	if guideSummary.All.N > 0 {
		log.Printf("PHD2: %d guide frames, %d sessions, RMS %.2f arcsec", guideSummary.All.N, guideSummary.Sessions, guideSummary.All.Total)
	}

	shots := fetchShots(client, report.PickShots(skyFrames, onset, hasOnset, sessionStart, sessionEnd))
	html, err := report.Render(report.Input{
		NightOf: night, Frames: res.Frames, Events: events, AFRuns: res.AFRuns,
		TargetChanges: res.TargetChanges, Untrustworthy: untrust,
		Onset: onset, HasOnset: hasOnset, CloudSource: cloud.Source,
		Sky: skyFrames, Shots: shots, RoofSpans: roofSpans, RoofFromLog: roofFromLog,
		Baselines: baselines, BaselineMode: mode,
		Flats: res.Flats, FlatExposures: res.FlatExposures,
		GuideBins: guideBins, Guide: guideSummary,
	})
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	if err := os.MkdirAll(cfg.ReportsDir, 0o755); err != nil {
		return err
	}
	out := filepath.Join(cfg.ReportsDir, night+".html")
	if err := os.WriteFile(out, html, 0o644); err != nil {
		return err
	}
	log.Printf("report written: %s (%d KB)", out, len(html)/1024)

	// Point neighbouring nights at this one and rebuild the index. Both
	// are local conveniences as well as what the server serves.
	files := []string{out}
	if changed, err := report.Relink(cfg.ReportsDir); err != nil {
		log.Printf("relink: %v", err)
	} else {
		files = append(files, changed...)
	}
	if idx, err := report.WriteIndex(cfg.ReportsDir); err != nil {
		log.Printf("index: %v", err)
	} else {
		files = append(files, idx)
	}

	// Publishing is a copy, not the product — failure is logged, not fatal.
	if err := publish.Files(cfg.Publish, files...); err != nil {
		log.Printf("publish FAILED (report is still available locally): %v", err)
	} else if cfg.Publish.Enabled {
		log.Printf("published %d files to %s:%s", len(files), cfg.Publish.Host, cfg.Publish.Dest)
	}
	return nil
}

// guideNight reads the PHD2 guide logs covering one night's window. Every
// failure returns an empty result with a log line: guiding is supporting
// evidence, and no report should fail because PHD2 was not running.
func guideNight(dir string, loc *time.Location, start, end time.Time) *phd2.Result {
	if dir == "" {
		return &phd2.Result{}
	}
	logs, err := phd2.FindNightLogs(dir, start, end, loc)
	if err != nil {
		log.Printf("no PHD2 guide logs in %s (continuing without guiding): %v", dir, err)
		return &phd2.Result{}
	}
	if len(logs) == 0 {
		return &phd2.Result{}
	}
	res, err := phd2.ParseFiles(logs, loc)
	if err != nil {
		log.Printf("PHD2 guide log unreadable (continuing without guiding): %v", err)
		return &phd2.Result{}
	}
	// One guide log can span two nights, so the night's window decides what
	// belongs to it, not the filename that matched.
	return res.Clip(start, end)
}

// ingestedFrom reports whether the night is on record from a log with this
// hash.
func ingestedFrom(dbPath, night, sha string) (bool, error) {
	sdb, err := store.Open(dbPath)
	if err != nil {
		return false, err
	}
	defer sdb.Close()
	prev, err := db.New(sdb).GetNight(context.Background(), night)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return prev.LogSha256 == sha, nil
}

// cloudVerdict is the night's cloud as attribution and the report use it.
// KnownAt is nil when the per-frame readings are not to hand (a verdict
// carried forward from the database), which Attribute reads as measured
// throughout.
type cloudVerdict struct {
	Onset    time.Time
	HasOnset bool
	Source   analysis.CloudSource
	KnownAt  func(time.Time) bool
}

// skycamClient is nil when allsky_base_url is empty, which leaves every
// night's cloud unknown.
func skycamClient(cfg *config.Config) *skycam.Client {
	if cfg.AllSkyBaseURL == "" {
		return nil
	}
	return &skycam.Client{
		BaseURL: cfg.AllSkyBaseURL, Username: cfg.AllSkyUsername, Password: cfg.AllSkyPassword,
		CacheDir: cfg.CacheDir,
	}
}

// skyNight reads one night from skycam's sky API. Every failure is a log
// line and a nil night, never an error: the report runs on the N.I.N.A. log
// alone, with cloud unknown.
func skyNight(c *skycam.Client, loc *time.Location, night string) *skycam.Night {
	if c == nil {
		log.Printf("no allsky_base_url configured — cloud unknown")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	n, err := c.Night(ctx, strings.ReplaceAll(night, "-", ""), loc)
	switch {
	case errors.Is(err, skycam.ErrNotFound):
		log.Printf("skycam has no frames for %s", night)
		return nil
	case err != nil:
		log.Printf("skycam sky API unavailable (continuing with cloud unknown): %v", err)
		return nil
	}
	log.Printf("skycam: %d frames for %s", len(n.Frames), night)
	return n
}

// fetchShots fetches the thumbnails' stills into the cache. skycam keeps
// stills for 14 nights, so a 404 is routine: that thumbnail is left out.
func fetchShots(c *skycam.Client, picks []report.ShotPick) []report.Shot {
	if c == nil || len(picks) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out []report.Shot
	for _, p := range picks {
		path, err := c.Still(ctx, p.Frame.Image)
		switch {
		case errors.Is(err, skycam.ErrNotFound):
			log.Printf("still %s is gone from skycam — no %s thumbnail", p.Frame.Image, p.Note)
			continue
		case err != nil:
			log.Printf("still %s: %v — no %s thumbnail", p.Frame.Image, err, p.Note)
			continue
		}
		out = append(out, report.Shot{At: p.Frame.At, Note: p.Note, Path: path})
	}
	return out
}

// chooseBaselines uses the rolling historical median once a pair has enough
// clear nights on record (excluding tonight); otherwise the night's own
// pre-cloud median bootstraps it, exactly as the prototype did.
//
// A pair with no clear frames tonight — a target that only began after the
// cloud onset — would otherwise have no baseline and so no index at all.
// For those, any clear history counts (even one night); failing that, the
// pair's whole-night median stands in and the report says the index for
// that target is relative only.
func chooseBaselines(ctx context.Context, q *db.Queries, frames []ninalog.Frame, onset time.Time, hasOnset bool, night string) (map[analysis.BaselineKey]analysis.Baseline, string) {
	self := analysis.Baselines(frames, onset, hasOnset)
	out := make(map[analysis.BaselineKey]analysis.Baseline, len(self))
	historical := 0
	for k, v := range self {
		out[k] = v
		if h, nights, ok := historicalBaseline(ctx, q, k, night); ok && nights >= minClearNightsForHistorical {
			out[k] = h
			historical++
		}
	}
	for _, k := range analysis.LightPairs(frames) {
		if _, ok := out[k]; ok {
			continue
		}
		if h, nights, ok := historicalBaseline(ctx, q, k, night); ok {
			log.Printf("WARNING: %s/%s has no clear frames tonight; baseline uses %d earlier night(s)", k.Target, k.Filter, nights)
			out[k] = h
			historical++
		}
	}
	for _, k := range analysis.FillWholeNight(frames, out) {
		log.Printf("WARNING: %s/%s has no clear frames tonight and no history; baseline uses the whole night including cloud", k.Target, k.Filter)
	}
	if historical == len(out) && historical > 0 {
		return out, "historical"
	}
	return out, "self"
}

// historicalBaseline is the rolling median of per-night medians over the
// most recent clear nights (at most 10) on record for one pair, excluding
// tonight. A night with fewer than MinBaselineFrames clear frames doesn't
// count. ok is false when there is no usable history.
func historicalBaseline(ctx context.Context, q *db.Queries, k analysis.BaselineKey, night string) (analysis.Baseline, int, bool) {
	rows, err := q.ListClearLightFrames(ctx, db.ListClearLightFramesParams{Target: k.Target, Filter: k.Filter})
	if err != nil {
		return analysis.Baseline{}, 0, false
	}
	perNight := map[string][]float64{}
	for _, r := range rows {
		if r.NightOf == night {
			continue
		}
		perNight[r.NightOf] = append(perNight[r.NightOf], float64(r.DetectedStars))
	}
	for n, v := range perNight {
		if len(v) < analysis.MinBaselineFrames {
			delete(perNight, n)
		}
	}
	if len(perNight) == 0 {
		return analysis.Baseline{}, 0, false
	}
	nights := make([]string, 0, len(perNight))
	for n := range perNight {
		nights = append(nights, n)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(nights)))
	if len(nights) > 10 {
		nights = nights[:10] // rolling window
	}
	var medians []float64
	frameCount := 0
	for _, n := range nights {
		medians = append(medians, analysis.MedianHigh(perNight[n]))
		frameCount += len(perNight[n])
	}
	return analysis.Baseline{MedianStars: analysis.MedianHigh(medians), NFrames: frameCount, Source: analysis.SourceHistorical}, len(nights), true
}

type ingestInput struct {
	night, logPath, sha string
	res                 *ninalog.Result
	events              []analysis.AttributedEvent
	onset               time.Time
	hasOnset            bool
	source              analysis.CloudSource
	baselines           map[analysis.BaselineKey]analysis.Baseline
	mode                string
}

// ingest writes the night atomically. Re-running on an unchanged log is a
// no-op (keyed on the sha); a changed log replaces the night's rows so
// development re-runs never double frames.
func ingest(ctx context.Context, sdb *sql.DB, q *db.Queries, in ingestInput) error {
	onsetStr := sql.NullString{}
	if in.hasOnset {
		onsetStr = sql.NullString{String: in.onset.UTC().Format(time.RFC3339), Valid: true}
	}
	if existing, err := q.GetNight(ctx, in.night); err == nil {
		// No-op only when both the log AND the cloud verdict are unchanged —
		// skycam can answer later than an early run, and a changed onset
		// changes every downstream number.
		if existing.LogSha256 == in.sha && existing.CloudOnsetAt == onsetStr && existing.CloudSource == string(in.source) {
			log.Printf("night %s already ingested (log and cloud unchanged)", in.night)
			return nil
		}
		log.Printf("night %s changed — replacing", in.night)
	}

	tx, err := sdb.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	qtx := q.WithTx(tx)

	if err := qtx.DeleteNight(ctx, in.night); err != nil {
		return err
	}
	if err := qtx.CreateNight(ctx, db.CreateNightParams{
		NightOf: in.night, LogPath: in.logPath, LogSha256: in.sha,
		CloudOnsetAt: onsetStr, CloudSource: string(in.source), BaselineMode: in.mode,
		IngestedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return err
	}
	night, err := qtx.GetNight(ctx, in.night)
	if err != nil {
		return err
	}

	for _, f := range in.res.Frames {
		idx := sql.NullFloat64{}
		if f.Class() == ninalog.ClassLight {
			if v, ok := analysis.Index(f, in.baselines); ok {
				idx = sql.NullFloat64{Float64: v, Valid: true}
			}
		}
		if err := qtx.InsertFrame(ctx, db.InsertFrameParams{
			NightID: night.ID, At: f.At.UTC().Format(time.RFC3339),
			Class: string(f.Class()), ExposureSec: f.ExposureSec,
			Filter: f.Filter, Target: f.Target,
			DetectedStars: int64(f.DetectedStars), Hfr: f.HFR, IndexPct: idx,
		}); err != nil {
			return err
		}
	}
	for _, e := range in.events {
		if err := qtx.InsertEvent(ctx, db.InsertEventParams{
			NightID: night.ID, At: e.At.UTC().Format(time.RFC3339),
			Kind: string(e.Kind), Detail: e.Detail, Cause: string(e.Cause),
		}); err != nil {
			return err
		}
	}
	if in.source == analysis.CloudUnknown {
		// Nothing says these frames saw clear sky, so they are not a
		// clear-sky reference for another night.
		return tx.Commit()
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for k, b := range in.baselines {
		if b.Source == analysis.SourceWholeNight {
			continue // a cloud-included divisor is not a clear-sky reference
		}
		if b.NFrames < analysis.MinBaselineFrames {
			continue // too few frames to trust on another night
		}
		if err := qtx.UpsertBaseline(ctx, db.UpsertBaselineParams{
			Target: k.Target, Filter: k.Filter, MedianStars: b.MedianStars,
			NFrames: int64(b.NFrames), NNights: 1, UpdatedAt: now,
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}
