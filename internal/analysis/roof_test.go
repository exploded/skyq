package analysis

import (
	"testing"
	"time"

	"github.com/exploded/skyq/internal/ninalog"
)

var testLoc = time.FixedZone("AEST", 10*3600)

func at(day int, hh, mm int) time.Time {
	return time.Date(2026, 9, day, hh, mm, 0, 0, testLoc)
}

func nightWindow() (time.Time, time.Time) { return at(2, 12, 0), at(3, 12, 0) }

// The validated night logs no open at all — the roof was opened by hand.
// Its only roof line is the morning close, and the "before closing
// ShutterOpen" half of it is what proves the night was open.
func TestRoofTimelineCarriesMorningCloseBackwards(t *testing.T) {
	from, to := nightWindow()
	tl := NewRoofTimeline([]ninalog.RoofMove{{
		Start: at(3, 6, 36), End: at(3, 6, 37),
		From: ninalog.RoofOpen, To: ninalog.RoofClosed,
	}}, from, to)

	if got := tl.StateAt(at(2, 21, 0)); got != ninalog.RoofOpen {
		t.Errorf("evening: got %s, want open", got)
	}
	if got := tl.StateAt(at(3, 3, 48)); got != ninalog.RoofOpen {
		t.Errorf("at the validated cloud onset: got %s, want open", got)
	}
	if got := tl.StateAt(at(3, 6, 36)); got != ninalog.RoofMoving {
		t.Errorf("during the close: got %s, want moving", got)
	}
	if got := tl.StateAt(at(3, 8, 0)); got != ninalog.RoofClosed {
		t.Errorf("after the close: got %s, want closed", got)
	}
}

// 2026-08-31/09-01 in the sample logs: shut at 01:20, reopened at 05:04.
// Those four hours are the camera looking at the roof.
func TestRoofTimelineMidNightClose(t *testing.T) {
	from := time.Date(2026, 8, 31, 12, 0, 0, 0, testLoc)
	to := at(1, 12, 0)
	tl := NewRoofTimeline([]ninalog.RoofMove{
		{Start: at(1, 1, 20), End: at(1, 1, 21), From: ninalog.RoofOpen, To: ninalog.RoofClosed},
		{Start: at(1, 5, 4), End: at(1, 5, 5), From: ninalog.RoofClosed, To: ninalog.RoofOpen},
	}, from, to)

	for _, tc := range []struct {
		at   time.Time
		want ninalog.RoofState
	}{
		{at(1, 0, 30), ninalog.RoofOpen},
		{at(1, 3, 0), ninalog.RoofClosed},
		{at(1, 6, 0), ninalog.RoofOpen},
	} {
		if got := tl.StateAt(tc.at); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.at.Format("15:04"), got, tc.want)
		}
	}

	// The shut hours, the move itself and the settle window are dropped;
	// everything else survives.
	var samples []LumSample
	for m := 0; m < 8*60; m += 10 {
		samples = append(samples, LumSample{At: at(1, 0, 0).Add(time.Duration(m) * time.Minute), Luminance: 20})
	}
	got := tl.VisibleSamples(samples, DefaultRoofSettle)
	for _, s := range got {
		if !s.At.Before(at(1, 1, 20)) && s.At.Before(at(1, 5, 10)) {
			t.Errorf("kept a roof-shut sample at %s", s.At.Format("15:04"))
		}
	}
	if len(got) == 0 || len(got) == len(samples) {
		t.Fatalf("filtered %d of %d samples; expected some but not all", len(samples)-len(got), len(samples))
	}
}

// Unknown must behave exactly as it did before the gate existed: a night
// with no roof lines keeps every sample. Blanking a night on a missing log
// line would lose real sky.
func TestRoofTimelineUnknownKeepsEverything(t *testing.T) {
	from, to := nightWindow()
	tl := NewRoofTimeline(nil, from, to)
	if got := tl.StateAt(at(3, 2, 0)); got != ninalog.RoofUnknown {
		t.Errorf("got %s, want unknown", got)
	}
	samples := []LumSample{{At: at(3, 2, 0), Luminance: 25}, {At: at(3, 3, 0), Luminance: 30}}
	if got := tl.VisibleSamples(samples, DefaultRoofSettle); len(got) != len(samples) {
		t.Errorf("kept %d of %d samples; want all", len(got), len(samples))
	}
}

// A move whose finish never reaches the log (a stall, or the log ending
// mid-move) stays moving rather than pretending it completed.
func TestRoofTimelineUnfinishedMove(t *testing.T) {
	from, to := nightWindow()
	tl := NewRoofTimeline([]ninalog.RoofMove{{
		Start: at(3, 6, 36), From: ninalog.RoofOpen,
	}}, from, to)
	if got := tl.StateAt(at(3, 9, 0)); got != ninalog.RoofMoving {
		t.Errorf("got %s, want moving", got)
	}
	if got := tl.StateAt(at(2, 22, 0)); got != ninalog.RoofOpen {
		t.Errorf("before the stall: got %s, want open", got)
	}
}

// The settle window after an open covers the roof edge crossing the frame
// and the lights still being on inside.
func TestRoofTimelineSettleAfterOpen(t *testing.T) {
	from, to := nightWindow()
	tl := NewRoofTimeline([]ninalog.RoofMove{{
		Start: at(2, 20, 16), End: at(2, 20, 17),
		From: ninalog.RoofClosed, To: ninalog.RoofOpen,
	}}, from, to)

	if tl.SkyVisible(at(2, 20, 19), DefaultRoofSettle) {
		t.Error("2 minutes after the open should still be settling")
	}
	if !tl.SkyVisible(at(2, 20, 30), DefaultRoofSettle) {
		t.Error("13 minutes after the open should be sky")
	}
}
