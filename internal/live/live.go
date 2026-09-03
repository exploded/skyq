// Package live is the Phase 2 engine: it polls the AllSky box for new
// stills, keeps the night's luminance series in memory, gates cloud
// detection on astronomical darkness and data freshness, and tails
// tonight's N.I.N.A. log for a live transparency index and event list.
//
// Safety (SPEC §8): everything here is advisory. The engine reports
// unknown — never "clear" — when data is stale or the sky isn't dark;
// consumers must treat unknown as "do not gate".
package live

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/exploded/riseset"

	"github.com/exploded/skyq/internal/allsky"
	"github.com/exploded/skyq/internal/analysis"
	"github.com/exploded/skyq/internal/ninalog"
)

// State is the live cloud verdict.
type State string

const (
	StateClear   State = "clear"
	StateCloudy  State = "cloudy"
	StateUnknown State = "unknown"
)

// Options configures the engine. Fetcher may be nil (cache-only; tests).
type Options struct {
	Fetcher     *allsky.Fetcher
	CacheDir    string
	Loc         *time.Location
	Latitude    float64
	Longitude   float64
	Window      int     // volatility trailing window (samples)
	Threshold   float64 // validated cloud threshold
	BandTop     float64
	BandBottom  float64
	MaxStillAge time.Duration
	Poll        time.Duration

	// NINALogDir enables the live index and event list when non-empty.
	NINALogDir string
	// Baselines supplies stored (target, filter) baselines for the live
	// index; called at most every 15 minutes. May be nil.
	Baselines func() map[analysis.BaselineKey]analysis.Baseline

	// Test hooks.
	Now        func() time.Time
	DarkWindow func(evening time.Time) (from, to time.Time, ok bool)
}

// Snapshot is what the Alpaca device and the live page read.
type Snapshot struct {
	At time.Time

	Cloud      State
	Reason     string  // why unknown; empty for clear/cloudy
	Volatility float64 // NaN until the window fills
	Threshold  float64

	Luminance   float64 // latest sample; NaN if none
	LastStill   string
	LastStillAt time.Time
	Stale       bool

	Dark     bool
	DarkFrom time.Time
	DarkTo   time.Time

	Samples []analysis.LumSample // tonight's, for the sparkline

	// Live transparency index (SkyQuality): median index of the last few
	// light frames against known baselines.
	Index   float64
	IndexOK bool

	Frames []ninalog.Frame
	Events []analysis.AttributedEvent
}

// Engine holds the night's state. Create with New, drive with Run.
type Engine struct {
	opt Options

	mu   sync.RWMutex
	snap Snapshot

	night        string // current YYYYMMDD stills dir
	samples      []analysis.LumSample
	seen         map[string]bool
	baselines    map[analysis.BaselineKey]analysis.Baseline
	baselinesAt  time.Time
	refresh      chan struct{}
}

func New(opt Options) *Engine {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.DarkWindow == nil {
		opt.DarkWindow = func(evening time.Time) (time.Time, time.Time, bool) {
			return darkWindow(evening, opt.Latitude, opt.Longitude, opt.Loc)
		}
	}
	e := &Engine{
		opt:     opt,
		seen:    map[string]bool{},
		refresh: make(chan struct{}, 1),
	}
	e.snap = Snapshot{Cloud: StateUnknown, Reason: "starting up", Threshold: opt.Threshold,
		Volatility: math.NaN(), Luminance: math.NaN()}
	return e
}

// Snapshot returns the current state (copy).
func (e *Engine) Snapshot() Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.snap
}

// Refresh asks the poller for an immediate pass (Alpaca's Refresh method).
func (e *Engine) Refresh() {
	select {
	case e.refresh <- struct{}{}:
	default:
	}
}

// PollOnce runs a single synchronous poll — used by tests and one-shot
// commissioning checks.
func (e *Engine) PollOnce(ctx context.Context) { e.poll(ctx) }

// Run polls until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	e.poll(ctx)
	t := time.NewTicker(e.opt.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.poll(ctx)
		case <-e.refresh:
			e.poll(ctx)
		}
	}
}

// nightOf gives the evening date for a moment (AllSky rolls at local noon).
func nightOf(t time.Time) time.Time {
	return t.Add(-12 * time.Hour).Truncate(24 * time.Hour)
}

func (e *Engine) poll(ctx context.Context) {
	now := e.opt.Now().In(e.opt.Loc)
	evening := now.Add(-12 * time.Hour)
	dateDir := evening.Format("20060102")

	// Night rollover at local noon: fresh series, fresh dedupe.
	if e.night != dateDir {
		e.night = dateDir
		e.samples = nil
		e.seen = map[string]bool{}
	}

	if e.opt.Fetcher != nil {
		sctx, cancel := context.WithTimeout(ctx, e.opt.Poll*4)
		fetched, err := e.opt.Fetcher.Sync(sctx, dateDir)
		cancel()
		if err != nil {
			log.Printf("live: stills sync incomplete: %v", err)
		}
		_ = fetched
	}
	e.measureNew(dateDir)

	darkFrom, darkTo, darkOK := e.opt.DarkWindow(evening)

	e.mu.Lock()
	defer e.mu.Unlock()
	s := Snapshot{
		At:        now,
		Threshold: e.opt.Threshold,
		DarkFrom:  darkFrom,
		DarkTo:    darkTo,
		Samples:   append([]analysis.LumSample(nil), e.samples...),
		Volatility: math.NaN(),
		Luminance:  math.NaN(),
		Index:      math.NaN(),
	}
	if n := len(e.samples); n > 0 {
		s.Luminance = e.samples[n-1].Luminance
		s.LastStillAt = e.samples[n-1].At
		s.LastStill = e.lastStillPath()
	}
	s.Stale = s.LastStillAt.IsZero() || now.Sub(s.LastStillAt) > e.opt.MaxStillAge
	s.Dark = darkOK && !now.Before(darkFrom) && now.Before(darkTo)

	// Cloud volatility only counts samples taken in darkness, so the
	// twilight luminance ramp can't fake a cloud right after dark.
	var darkSamples []analysis.LumSample
	if darkOK {
		for _, smp := range e.samples {
			if !smp.At.Before(darkFrom) {
				darkSamples = append(darkSamples, smp)
			}
		}
	}
	if len(darkSamples) >= e.opt.Window {
		vol := analysis.VolatilitySeries(darkSamples, e.opt.Window)
		s.Volatility = vol[len(vol)-1]
	}

	switch {
	case s.Stale:
		s.Cloud = StateUnknown
		s.Reason = "no recent still from the all-sky camera"
	case !darkOK:
		s.Cloud = StateUnknown
		s.Reason = "no darkness window tonight"
	case !s.Dark:
		s.Cloud = StateUnknown
		s.Reason = fmt.Sprintf("waiting for darkness (%s–%s)", darkFrom.Format("15:04"), darkTo.Format("15:04"))
	case math.IsNaN(s.Volatility):
		s.Cloud = StateUnknown
		s.Reason = fmt.Sprintf("warming up (%d of %d dark samples)", len(darkSamples), e.opt.Window)
	case s.Volatility > e.opt.Threshold:
		s.Cloud = StateCloudy
	default:
		s.Cloud = StateClear
	}

	e.tailLog(&s, evening, now, darkFrom, darkOK)
	e.snap = s
}

// measureNew computes luminance for cache stills not yet seen.
func (e *Engine) measureNew(dateDir string) {
	stills, err := allsky.ScanDir(e.cacheNight(dateDir), e.opt.Loc)
	if err != nil {
		return // no cache dir yet — nothing captured tonight
	}
	for _, st := range stills {
		if e.seen[st.Path] {
			continue
		}
		f, err := os.Open(st.Path)
		if err != nil {
			continue
		}
		lum, err := allsky.Luminance(f, e.opt.BandTop, e.opt.BandBottom)
		f.Close()
		if err != nil {
			log.Printf("live: luminance %s: %v", st.Path, err)
			e.seen[st.Path] = true // don't retry a broken file forever
			continue
		}
		e.seen[st.Path] = true
		e.samples = append(e.samples, analysis.LumSample{At: st.At, Luminance: lum})
	}
	sort.Slice(e.samples, func(i, j int) bool { return e.samples[i].At.Before(e.samples[j].At) })
}

func (e *Engine) cacheNight(dateDir string) string {
	return e.opt.CacheDir + string(os.PathSeparator) + dateDir
}

func (e *Engine) lastStillPath() string {
	stills, err := allsky.ScanDir(e.cacheNight(e.night), e.opt.Loc)
	if err != nil || len(stills) == 0 {
		return ""
	}
	return stills[len(stills)-1].Path
}

// tailLog re-parses tonight's N.I.N.A. log for the live index and events.
func (e *Engine) tailLog(s *Snapshot, evening, now, darkFrom time.Time, darkOK bool) {
	if e.opt.NINALogDir == "" {
		return
	}
	nightStart := time.Date(evening.Year(), evening.Month(), evening.Day(), 12, 0, 0, 0, e.opt.Loc)
	logs, err := ninalog.FindNightLogs(e.opt.NINALogDir, nightStart, nightStart.Add(24*time.Hour), e.opt.Loc)
	if err != nil || len(logs) == 0 {
		return
	}
	res, _, err := ninalog.ParseFiles(logs, e.opt.Loc)
	if err != nil {
		log.Printf("live: parsing tonight's log: %v", err)
		return
	}
	s.Frames = res.Frames

	onset, hasOnset := analysis.DetectOnset(darkSamplesOnly(s.Samples, darkFrom, darkOK), e.opt.Window, e.opt.Threshold)
	s.Events = analysis.Attribute(res.Events, res.Slews, res.TargetChanges, onset, hasOnset)

	// Live index: median index of the last few light frames against known
	// baselines — rolling historical if the store has them, else tonight's
	// own running medians.
	base := e.loadBaselines(now)
	if len(base) == 0 {
		base = analysis.Baselines(res.Frames, onset, hasOnset)
	}
	var idx []float64
	for _, f := range res.Frames {
		if f.Class() != ninalog.ClassLight {
			continue
		}
		if v, ok := analysis.Index(f, base); ok {
			idx = append(idx, v)
		}
	}
	const recent = 6
	if len(idx) > 0 {
		if len(idx) > recent {
			idx = idx[len(idx)-recent:]
		}
		s.Index = analysis.Summarise(idx).Median
		s.IndexOK = true
	}
}

func darkSamplesOnly(samples []analysis.LumSample, darkFrom time.Time, darkOK bool) []analysis.LumSample {
	if !darkOK {
		return nil
	}
	var out []analysis.LumSample
	for _, s := range samples {
		if !s.At.Before(darkFrom) {
			out = append(out, s)
		}
	}
	return out
}

func (e *Engine) loadBaselines(now time.Time) map[analysis.BaselineKey]analysis.Baseline {
	if e.opt.Baselines == nil {
		return nil
	}
	if e.baselines == nil || now.Sub(e.baselinesAt) > 15*time.Minute {
		e.baselines = e.opt.Baselines()
		e.baselinesAt = now
	}
	return e.baselines
}

// darkWindow returns tonight's astronomical-dark span for the given evening
// date: nautical twilight end that evening to nautical twilight start next
// morning. ok is false in polar conditions or if riseset yields no times.
func darkWindow(evening time.Time, lat, lon float64, loc *time.Location) (time.Time, time.Time, bool) {
	eveDate := time.Date(evening.Year(), evening.Month(), evening.Day(), 0, 0, 0, 0, loc)
	nextDate := eveDate.AddDate(0, 0, 1)

	from, ok1 := twilightEvent(eveDate, lat, lon, loc, false)
	to, ok2 := twilightEvent(nextDate, lat, lon, loc, true)
	if !ok1 || !ok2 || !to.After(from) {
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

// twilightEvent returns the twilight rise (morning) or set (evening) time
// on the given date.
func twilightEvent(date time.Time, lat, lon float64, loc *time.Location, rise bool) (time.Time, bool) {
	_, offset := date.In(loc).Zone()
	r := riseset.Riseset(riseset.Twilight, date, lon, lat, float64(offset)/3600)
	v := r.Set
	if rise {
		v = r.Rise
	}
	// riseset v1.0.0 puts placeholder text in Rise/Set when there is no
	// event (polar conditions); any non-hh:mm content means no window.
	var hh, mm int
	if _, err := fmt.Sscanf(v, "%d:%d", &hh, &mm); err != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return time.Time{}, false
	}
	return time.Date(date.Year(), date.Month(), date.Day(), hh, mm, 0, 0, loc), true
}
