package live

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exploded/skyq/internal/ninalog"
)

// writeStill drops a uniform-grey synthetic still into the cache dir with
// an AllSky-style filename.
func writeStill(t *testing.T, dir string, at time.Time, level uint8) {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 64, 48))
	for i := range img.Pix {
		img.Pix[i] = level
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	name := "image-" + at.Format("20060102150405") + ".jpg"
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

type fixture struct {
	engine *Engine
	now    time.Time
	dir    string
}

// newFixture builds an engine over a temp cache with a controllable clock
// and a fixed dark window (19:00 → 05:30).
func newFixture(t *testing.T, tweak ...func(*Options)) *fixture {
	t.Helper()
	f := &fixture{}
	cache := t.TempDir()
	f.dir = filepath.Join(cache, "20260902")
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	darkFrom := time.Date(2026, 9, 2, 19, 0, 0, 0, time.UTC)
	darkTo := time.Date(2026, 9, 3, 5, 30, 0, 0, time.UTC)
	opt := Options{
		CacheDir: cache, Loc: time.UTC,
		Window: 5, Threshold: 6.0,
		BandTop: 0.06, BandBottom: 0.30,
		MaxStillAge: 5 * time.Minute,
		Poll:        time.Minute,
		Now:         func() time.Time { return f.now },
		DarkWindow: func(time.Time) (time.Time, time.Time, bool) {
			return darkFrom, darkTo, true
		},
	}
	for _, fn := range tweak {
		fn(&opt)
	}
	f.engine = New(opt)
	return f
}

// writeRoofLog drops a minimal N.I.N.A. log holding one roof move into a
// temp dir and points the engine at it.
func writeRoofLog(t *testing.T, o *Options, start, end time.Time, opening bool) {
	t.Helper()
	dir := t.TempDir()
	// N.I.N.A. logs "CloseShutter"/"OpenShutter" as the method, with its own
	// tense in the message and the shutter state on each side of the move.
	method, verb, past, prep, from, to := "Close", "Closing", "Closed", "closing", "Open", "Closed"
	if opening {
		method, verb, past, prep, from, to = "Open", "Opening", "Opened", "opening", "Closed", "Open"
	}
	body := fmt.Sprintf(
		"%s|INFO|DomeVM.cs|%sShutter|544|%s dome shutter. Shutter state before %s Shutter%s\n"+
			"%s|INFO|DomeVM.cs|%sShutter|550|%s dome shutter. Shutter state after %s Shutter%s\n",
		start.Format("2006-01-02T15:04:05.000"), method, verb, prep, from,
		end.Format("2006-01-02T15:04:05.000"), method, past, prep, to)
	name := "20260902-190000-3.2.0.9001.1-202609.log"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	o.NINALogDir = dir
	o.RoofSettle = time.Minute
}

func TestStartupIsUnknown(t *testing.T) {
	f := newFixture(t)
	if s := f.engine.Snapshot(); s.Cloud != StateUnknown {
		t.Errorf("before first poll state = %s, want unknown", s.Cloud)
	}
}

func TestTwilightGate(t *testing.T) {
	f := newFixture(t)
	// Fresh stills, but the sun isn't down yet.
	f.now = time.Date(2026, 9, 2, 18, 30, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		writeStill(t, f.dir, f.now.Add(time.Duration(i-8)*time.Minute), 120)
	}
	f.engine.PollOnce(context.Background())
	s := f.engine.Snapshot()
	if s.Cloud != StateUnknown || !strings.Contains(s.Reason, "darkness") {
		t.Errorf("twilight: state=%s reason=%q, want unknown/waiting for darkness", s.Cloud, s.Reason)
	}
	if math.IsNaN(s.Luminance) {
		t.Error("luminance should still be reported in twilight")
	}
}

func TestClearAndCloudy(t *testing.T) {
	f := newFixture(t)
	base := time.Date(2026, 9, 2, 21, 0, 0, 0, time.UTC)
	// Twilight-era wild samples must NOT poison the dark-window volatility.
	writeStill(t, f.dir, base.Add(-150*time.Minute), 250)
	writeStill(t, f.dir, base.Add(-140*time.Minute), 30)
	// Flat luminance through darkness → clear.
	for i := 0; i < 6; i++ {
		writeStill(t, f.dir, base.Add(time.Duration(i)*time.Minute), 28)
	}
	f.now = base.Add(6 * time.Minute)
	f.engine.PollOnce(context.Background())
	if s := f.engine.Snapshot(); s.Cloud != StateClear {
		t.Fatalf("flat dark series: state=%s reason=%q vol=%.2f, want clear", s.Cloud, s.Reason, s.Volatility)
	}
	// Violent swings → cloudy.
	for i := 6; i < 12; i++ {
		level := uint8(28)
		if i%2 == 0 {
			level = 90
		}
		writeStill(t, f.dir, base.Add(time.Duration(i)*time.Minute), level)
	}
	f.now = base.Add(12 * time.Minute)
	f.engine.PollOnce(context.Background())
	if s := f.engine.Snapshot(); s.Cloud != StateCloudy {
		t.Fatalf("swinging series: state=%s vol=%.2f, want cloudy", s.Cloud, s.Volatility)
	}
}

func TestStaleIsUnknownNeverClear(t *testing.T) {
	f := newFixture(t)
	base := time.Date(2026, 9, 2, 21, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		writeStill(t, f.dir, base.Add(time.Duration(i)*time.Minute), 28)
	}
	// Camera dies: clock advances well past max still age.
	f.now = base.Add(30 * time.Minute)
	f.engine.PollOnce(context.Background())
	s := f.engine.Snapshot()
	if s.Cloud != StateUnknown || !s.Stale {
		t.Errorf("stale: state=%s stale=%v, want unknown/stale — a dead camera must never read as clear", s.Cloud, s.Stale)
	}
}

func TestDarkWindowMelbourne(t *testing.T) {
	loc, err := time.LoadLocation("Australia/Melbourne")
	if err != nil {
		t.Skip("no tzdata")
	}
	evening := time.Date(2026, 9, 2, 20, 0, 0, 0, loc)
	from, to, ok := darkWindow(evening, -37.8, 145.0, loc)
	if !ok {
		t.Fatal("no dark window for Melbourne in September")
	}
	if from.Hour() < 18 || from.Hour() > 20 {
		t.Errorf("dark start %s, expected evening nautical twilight ~19:00", from.Format("15:04"))
	}
	if to.Hour() < 4 || to.Hour() > 7 {
		t.Errorf("dark end %s, expected morning nautical twilight ~05:30", to.Format("15:04"))
	}
	if !to.After(from) || to.Sub(from) > 16*time.Hour {
		t.Errorf("implausible dark window %s → %s", from, to)
	}
}

// The failure that motivated the roof gate: the underside of a shut roof is
// almost perfectly constant, so a closed roof scores near-zero volatility
// and would otherwise publish CLEAR over Alpaca while the observatory is
// buttoned up.
func TestRoofClosedIsNeverClear(t *testing.T) {
	closeStart := time.Date(2026, 9, 2, 20, 55, 0, 0, time.UTC)
	f := newFixture(t, func(o *Options) {
		writeRoofLog(t, o, closeStart, closeStart.Add(30*time.Second), false)
	})
	base := time.Date(2026, 9, 2, 21, 0, 0, 0, time.UTC)
	// Flat and dark — exactly what a shut roof looks like.
	for i := 0; i < 8; i++ {
		writeStill(t, f.dir, base.Add(time.Duration(i)*time.Minute), 6)
	}
	f.now = base.Add(8 * time.Minute)
	f.engine.PollOnce(context.Background())

	s := f.engine.Snapshot()
	if s.Cloud != StateUnknown {
		t.Errorf("roof shut: state=%s vol=%.2f, want unknown", s.Cloud, s.Volatility)
	}
	if !strings.Contains(s.Reason, "roof") {
		t.Errorf("reason = %q, want it to name the roof", s.Reason)
	}
	if s.RoofExcluded != 8 {
		t.Errorf("excluded %d samples, want all 8", s.RoofExcluded)
	}
}

// Opening the roof is a bigger luminance step than any cloud. Dropping the
// shut-roof samples keeps that step out of the trailing window, so arriving
// at the observatory doesn't fabricate a cloud onset.
func TestRoofOpeningIsNotCloud(t *testing.T) {
	openStart := time.Date(2026, 9, 2, 21, 6, 0, 0, time.UTC)
	f := newFixture(t, func(o *Options) {
		writeRoofLog(t, o, openStart, openStart.Add(30*time.Second), true)
	})
	base := time.Date(2026, 9, 2, 21, 0, 0, 0, time.UTC)
	// Six minutes of shut roof, then the step, then flat sky.
	for i := 0; i < 6; i++ {
		writeStill(t, f.dir, base.Add(time.Duration(i)*time.Minute), 6)
	}
	for i := 8; i < 16; i++ {
		writeStill(t, f.dir, base.Add(time.Duration(i)*time.Minute), 34)
	}
	f.now = base.Add(16 * time.Minute)
	f.engine.PollOnce(context.Background())

	s := f.engine.Snapshot()
	if s.Cloud == StateCloudy {
		t.Errorf("the roof opening read as cloud: vol=%.2f threshold=%.1f", s.Volatility, s.Threshold)
	}
	if s.Roof != ninalog.RoofOpen {
		t.Errorf("roof = %s, want open", s.Roof)
	}
	if s.RoofExcluded != 6 {
		t.Errorf("excluded %d samples, want the 6 taken with the roof shut", s.RoofExcluded)
	}
}

// A night the log says nothing about must behave exactly as it did before
// the gate existed. N.I.N.A. never sees a roof opened by hand, so unknown
// can't be allowed to mean shut.
func TestRoofUnknownStillReportsCloud(t *testing.T) {
	f := newFixture(t)
	base := time.Date(2026, 9, 2, 21, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		level := uint8(28)
		if i%2 == 0 {
			level = 90
		}
		writeStill(t, f.dir, base.Add(time.Duration(i)*time.Minute), level)
	}
	f.now = base.Add(12 * time.Minute)
	f.engine.PollOnce(context.Background())

	s := f.engine.Snapshot()
	if s.Cloud != StateCloudy {
		t.Errorf("no roof history: state=%s reason=%q, want cloudy as before", s.Cloud, s.Reason)
	}
	if s.RoofExcluded != 0 {
		t.Errorf("excluded %d samples with no roof history, want 0", s.RoofExcluded)
	}
}
