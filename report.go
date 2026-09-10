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

	"github.com/exploded/skyq/internal/allsky"
	"github.com/exploded/skyq/internal/analysis"
	"github.com/exploded/skyq/internal/config"
	"github.com/exploded/skyq/internal/ninalog"
	"github.com/exploded/skyq/internal/publish"
	"github.com/exploded/skyq/internal/report"
	"github.com/exploded/skyq/internal/store"
	db "github.com/exploded/skyq/internal/store/db"
)

var errNoLogs = errors.New("no N.I.N.A. logs for that night")

// minClearNightsForHistorical is when a (target, filter) baseline switches
// from the night's own pre-cloud median to a rolling median across nights.
const minClearNightsForHistorical = 5

func runReport(cfg *config.Config, loc *time.Location, night string) error {
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

	// All-sky stills: sync from allsky.local (best effort — the analysis
	// still runs on the log alone if the box is unreachable), then measure.
	// Sampling is clamped to the imaging session: AllSky records from dusk,
	// and the twilight luminance ramp reads as volatility, firing a false
	// cloud onset before the first frame (seen on real data: "onset" 20:37).
	sampleStart, sampleEnd := res.Frames[0].At, res.Frames[len(res.Frames)-1].At.Add(30*time.Minute)
	if n := len(res.Flats); n > 0 {
		// Dawn flats can run past the last light frame; cover them so the
		// unstable-sky check below has data.
		if end := res.Flats[n-1].At.Add(10 * time.Minute); end.After(sampleEnd) {
			sampleEnd = end
		}
	}
	samples, stills := skySamples(cfg, loc, night, sampleStart, sampleEnd)

	// The all-sky camera is inside the observatory: with the roof shut it
	// measures the roof, near-constant and dark. Those stills must not reach
	// the volatility window, or a dawn close reads as a cloud onset and a
	// mid-night close reads as the clearest hours of the night. The log only
	// records roof moves N.I.N.A. commanded, so nights it says nothing about
	// are analysed exactly as before (SPEC §4.4).
	roof := analysis.NewRoofTimeline(res.RoofMoves, windowStart, windowEnd)
	roofNote := ""
	if shut := len(samples) - len(roof.VisibleSamples(samples, analysis.DefaultRoofSettle)); shut > 0 {
		log.Printf("roof shut or moving for %d of %d stills — excluded from cloud detection", shut, len(samples))
		roofNote = roofShutNote(roof, sampleStart, sampleEnd, shut)
		samples = roof.VisibleSamples(samples, analysis.DefaultRoofSettle)
	}
	onset, hasOnset := analysis.DetectOnset(samples, cfg.VolatilityWindow, cfg.VolatilityThreshold)
	if hasOnset {
		log.Printf("cloud onset %s (volatility > %.1f)", onset.Format("15:04"), cfg.VolatilityThreshold)
	}

	// Dawn cloud during sky flats puts gradients in them — worth a warning
	// while the frames are still easy to retake.
	flatsUnstable := false
	if len(res.Flats) > 0 {
		vol := analysis.VolatilitySeries(samples, cfg.VolatilityWindow)
		f0, f1 := res.Flats[0].At, res.Flats[len(res.Flats)-1].At
		for i, s := range samples {
			if !s.At.Before(f0) && !s.At.After(f1) && vol[i] > cfg.VolatilityThreshold {
				flatsUnstable = true
				break
			}
		}
		if flatsUnstable {
			log.Printf("WARNING: unstable sky during flats (%s–%s)", f0.Format("15:04"), f1.Format("15:04"))
		}
	}

	sdb, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer sdb.Close()
	q := db.New(sdb)
	ctx := context.Background()

	baselines, mode := chooseBaselines(ctx, q, res.Frames, onset, hasOnset, night)
	log.Printf("baseline mode: %s (%d pairs)", mode, len(baselines))

	events := analysis.Attribute(res.Events, res.Slews, res.TargetChanges, onset, hasOnset)
	var untrust []ninalog.AFRun
	for _, r := range res.AFRuns {
		if analysis.UntrustworthyAF(r, res.Frames) {
			untrust = append(untrust, r)
		}
	}

	if err := ingest(ctx, sdb, q, ingestInput{
		night: night, logPath: strings.Join(logs, ";"), sha: sha,
		res: res, events: events, samples: samples,
		onset: onset, hasOnset: hasOnset, baselines: baselines, mode: mode,
		volWindow: cfg.VolatilityWindow,
	}); err != nil {
		return fmt.Errorf("ingest: %w", err)
	}

	html, err := report.Render(report.Input{
		NightOf: night, Frames: res.Frames, Events: events, AFRuns: res.AFRuns,
		TargetChanges: res.TargetChanges, Samples: samples,
		Onset: onset, HasOnset: hasOnset,
		Baselines: baselines, BaselineMode: mode,
		Stills: stills, Untrustworthy: untrust,
		Flats: res.Flats, FlatExposures: res.FlatExposures, FlatsUnstableSky: flatsUnstable,
		RoofNote: roofNote,
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

// skySamples syncs the night's stills into the cache and measures them.
func skySamples(cfg *config.Config, loc *time.Location, night string, start, end time.Time) ([]analysis.LumSample, []allsky.Still) {
	dateDir := strings.ReplaceAll(night, "-", "")
	if cfg.AllSkyBaseURL != "" {
		f := &allsky.Fetcher{
			BaseURL: cfg.AllSkyBaseURL, Username: cfg.AllSkyUsername,
			Password: cfg.AllSkyPassword, CacheDir: cfg.CacheDir,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		fetched, err := f.Sync(ctx, dateDir)
		if len(fetched) > 0 {
			log.Printf("fetched %d new stills", len(fetched))
		}
		if err != nil {
			log.Printf("stills sync from %s incomplete (continuing with what's cached): %v", cfg.AllSkyBaseURL, err)
		}
	}
	all, err := allsky.ScanDir(filepath.Join(cfg.CacheDir, dateDir), loc)
	if err != nil {
		log.Printf("no stills cache for %s: %v", night, err)
		return nil, nil
	}
	var samples []analysis.LumSample
	var stills []allsky.Still
	for _, s := range all {
		if s.At.Before(start) || !s.At.Before(end) {
			continue
		}
		fh, err := os.Open(s.Path)
		if err != nil {
			continue
		}
		lum, err := allsky.Luminance(fh, cfg.BandTop, cfg.BandBottom)
		fh.Close()
		if err != nil {
			log.Printf("luminance %s: %v", filepath.Base(s.Path), err)
			continue
		}
		samples = append(samples, analysis.LumSample{At: s.At, Luminance: lum})
		stills = append(stills, s)
	}
	return samples, stills
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
// tonight. ok is false when there is no history at all.
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
	samples             []analysis.LumSample
	onset               time.Time
	hasOnset            bool
	baselines           map[analysis.BaselineKey]analysis.Baseline
	mode                string
	volWindow           int
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
		// No-op only when both the log AND the all-sky conclusion are
		// unchanged — stills keep arriving after early re-runs, and a
		// changed onset changes every downstream number.
		if existing.LogSha256 == in.sha && existing.CloudOnsetAt == onsetStr {
			log.Printf("night %s already ingested (log and onset unchanged)", in.night)
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
		CloudOnsetAt: onsetStr, BaselineMode: in.mode,
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
	vol := analysis.VolatilitySeries(in.samples, in.volWindow)
	for i, s := range in.samples {
		v := sql.NullFloat64{}
		if !isNaN(vol[i]) {
			v = sql.NullFloat64{Float64: vol[i], Valid: true}
		}
		if err := qtx.InsertSkySample(ctx, db.InsertSkySampleParams{
			NightID: night.ID, At: s.At.UTC().Format(time.RFC3339),
			Luminance: s.Luminance, Volatility: v, ImagePath: "",
		}); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for k, b := range in.baselines {
		if b.Source == analysis.SourceWholeNight {
			continue // a cloud-included divisor is not a clear-sky reference
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

func isNaN(f float64) bool { return f != f }

func runCalibrate(cfg *config.Config, loc *time.Location, night string) error {
	nightStart, err := time.ParseInLocation("2006-01-02", night, loc)
	if err != nil {
		return fmt.Errorf("bad night %q: %w", night, err)
	}
	start := nightStart.Add(12 * time.Hour)
	samples, _ := skySamples(cfg, loc, night, start, start.Add(24*time.Hour))
	if len(samples) < cfg.VolatilityWindow*2 {
		return fmt.Errorf("only %d samples for %s — need a full night of stills", len(samples), night)
	}
	vol := analysis.VolatilitySeries(samples, cfg.VolatilityWindow)
	var vals []float64
	for _, v := range vol {
		if !isNaN(v) {
			vals = append(vals, v)
		}
	}
	sort.Float64s(vals)
	max := vals[len(vals)-1]
	p99 := vals[len(vals)*99/100]
	fmt.Printf("night %s: %d samples\n", night, len(samples))
	fmt.Printf("volatility max %.2f, p99 %.2f (current threshold %.2f)\n", max, p99, cfg.VolatilityThreshold)
	fmt.Printf("suggested threshold for a KNOWN CLEAR night: %.1f (max × 1.5)\n", max*1.5)
	fmt.Println("only calibrate against a night you know was clear end to end.")
	return nil
}

// roofShutNote describes the gaps the roof gate leaves in the luminance
// chart. Without it a reader sees a hole in the night and assumes the
// camera failed.
func roofShutNote(roof analysis.RoofTimeline, from, to time.Time, excluded int) string {
	var spans []string
	for _, sp := range roof.Spans() {
		if sp.State != ninalog.RoofClosed || !sp.To.After(from) || !sp.From.Before(to) {
			continue
		}
		start, end := sp.From, sp.To
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		spans = append(spans, fmt.Sprintf("%s–%s", start.Format("15:04"), end.Format("15:04")))
	}
	if len(spans) == 0 {
		return ""
	}
	return fmt.Sprintf("Roof shut %s. The all-sky camera is inside the observatory, so those %d stills measured the roof, not the sky, and are left out of this chart and the cloud detection.",
		strings.Join(spans, ", "), excluded)
}
