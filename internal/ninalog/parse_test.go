package ninalog

import (
	"os"
	"strings"
	"testing"
	"time"
)

// The assertions in this file are §9 of SPEC.md: hand-validated numbers from
// the night of 2026-09-02/03. The parser must reproduce them exactly.

func parseFixture(t *testing.T) *Result {
	t.Helper()
	f, err := os.Open("../../testdata/20260902.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := Parse(f, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.ParseInLocation("2006-01-02T15:04:05", s, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestFrameClassCounts(t *testing.T) {
	res := parseFixture(t)
	counts := map[Class]int{}
	byFilter := map[string]int{}
	for _, f := range res.Frames {
		counts[f.Class()]++
		if f.Class() == ClassLight {
			byFilter[f.Filter]++
		}
	}
	if counts[ClassLight] != 79 {
		t.Errorf("light frames = %d, want 79", counts[ClassLight])
	}
	if counts[ClassCentering] != 18 {
		t.Errorf("centering frames = %d, want 18", counts[ClassCentering])
	}
	want := map[string]int{"H": 32, "O": 25, "S": 22}
	for filt, n := range want {
		if byFilter[filt] != n {
			t.Errorf("light frames filter %s = %d, want %d", filt, byFilter[filt], n)
		}
	}
	for filt := range byFilter {
		if _, ok := want[filt]; !ok {
			t.Errorf("unexpected light-frame filter %q (%d frames)", filt, byFilter[filt])
		}
	}
}

func TestAutofocusRuns(t *testing.T) {
	res := parseFixture(t)
	if got := len(res.AFRuns); got != 14 {
		t.Errorf("completed autofocus runs = %d, want 14", got)
	}
	failed := 0
	for _, e := range res.Events {
		if e.Kind == AutofocusFailed {
			failed++
		}
	}
	if failed != 3 {
		t.Errorf("failed autofocus runs = %d, want 3", failed)
	}
	// The 04:42 run rejected its own first answer.
	var found bool
	for _, r := range res.AFRuns {
		if r.Start.Format("15:04") == "04:42" {
			found = true
			if !r.Rejected {
				t.Error("04:42 autofocus run should be flagged Rejected")
			}
		}
	}
	if !found {
		t.Error("no autofocus run starting 04:42")
	}
}

func TestTargetsAndSlews(t *testing.T) {
	res := parseFixture(t)
	if len(res.TargetChanges) != 2 {
		t.Fatalf("target changes = %d, want 2 (%v)", len(res.TargetChanges), res.TargetChanges)
	}
	if res.TargetChanges[0].Name != "IC 4628" || res.TargetChanges[1].Name != "NGC 2070" {
		t.Errorf("targets = %q, %q; want IC 4628 then NGC 2070",
			res.TargetChanges[0].Name, res.TargetChanges[1].Name)
	}
	// The slew to NGC 2070 at 00:10:39 anchors post-slew event attribution.
	slewAt := at(t, "2026-09-03T00:10:39")
	var found bool
	for _, s := range res.Slews {
		if s.At.Truncate(time.Second).Equal(slewAt) {
			found = true
		}
	}
	if !found {
		t.Errorf("no slew at 00:10:39; slews: %v", res.Slews)
	}
	// No light frame carries the second target before that slew.
	for _, f := range res.Frames {
		if f.Class() == ClassLight && f.Target == "NGC 2070" && f.At.Before(slewAt) {
			t.Errorf("light frame at %s tagged NGC 2070 before the 00:10:39 slew", f.At.Format("15:04:05"))
		}
	}
}

func TestFlats(t *testing.T) {
	res := parseFixture(t)
	byFilter := map[string]int{}
	for _, fl := range res.Flats {
		byFilter[fl.Filter]++
	}
	if len(res.Flats) != 60 {
		t.Errorf("flats = %d, want 60", len(res.Flats))
	}
	for _, f := range []string{"H", "O", "S"} {
		if byFilter[f] != 20 {
			t.Errorf("flats filter %s = %d, want 20", f, byFilter[f])
		}
	}
	if len(res.FlatExposures) != 3 {
		t.Errorf("flat exposures found = %d, want 3 (%v)", len(res.FlatExposures), res.FlatExposures)
	}
	for _, e := range res.Events {
		if e.Kind == FlatsFailed {
			t.Errorf("unexpected flats failure event at %s — all three searches converged", e.At.Format("15:04:05"))
		}
	}
	if first := res.Flats[0].At.Format("15:04"); first != "06:30" {
		t.Errorf("first flat at %s, want 06:30", first)
	}
}

func TestFailureEvents(t *testing.T) {
	res := parseFixture(t)
	counts := map[EventKind]int{}
	for _, e := range res.Events {
		counts[e.Kind]++
	}
	if counts[SolveFailed] != 6 {
		t.Errorf("plate-solve failures = %d, want 6", counts[SolveFailed])
	}
	if counts[PHD2Error] != 6 {
		t.Errorf("PHD2 errors = %d, want 6", counts[PHD2Error])
	}
	if counts[DomeRefused] == 0 {
		t.Error("expected dome-refused events")
	}
	// The one PHD2 error that is not weather: 00:17:12, ten minutes after the slew.
	var found bool
	for _, e := range res.Events {
		if e.Kind == PHD2Error && e.At.Format("15:04:05") == "00:17:12" {
			found = true
		}
	}
	if !found {
		t.Error("no PHD2 error at 00:17:12")
	}
}

// A long exposure's capture line leaves the Filter field empty, so the
// parser falls back to the last known wheel position. That position must
// also be learned from short-exposure capture lines that do name the filter:
// on 2026-09-12 the wheel was already on L when the sequence began, N.I.N.A.
// never logged a ChangeFilter, and the first four lights had no filter.
func TestFilterLatchesFromCaptureLine(t *testing.T) {
	log := strings.Join([]string{
		"2026-09-12T21:16:37.0000|INFO|CameraVM.cs|Capture|742|Starting Exposure - Exposure Time: 5s; Filter: L; Gain: 100; Offset 50; Binning: 2x2;",
		"2026-09-12T21:16:44.0000|INFO|HocusFocusStarDetection.cs|BuildStarDetectionResult|818|Average HFR: 2.5, HFR MAD: 0.3, Detected Stars 649, Region: 0",
		"2026-09-12T21:25:54.0000|INFO|CameraVM.cs|Capture|742|Starting Exposure - Exposure Time: 300s; Filter: ; Gain: 100; Offset 50; Binning: 1x1;",
		"2026-09-12T21:30:56.0000|INFO|HocusFocusStarDetection.cs|BuildStarDetectionResult|818|Average HFR: 3.4, HFR MAD: 0.3, Detected Stars 1370, Region: 0",
		"2026-09-12T21:31:00.0000|INFO|FilterWheelVM.cs|ChangeFilter|112|Moving to Filter R at Position 2",
		"2026-09-12T21:31:02.0000|INFO|CameraVM.cs|Capture|742|Starting Exposure - Exposure Time: 300s; Filter: ; Gain: 100; Offset 50; Binning: 1x1;",
		"2026-09-12T21:36:04.0000|INFO|HocusFocusStarDetection.cs|BuildStarDetectionResult|818|Average HFR: 4.0, HFR MAD: 0.3, Detected Stars 1160, Region: 0",
	}, "\n")
	res, err := Parse(strings.NewReader(log), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Frames) != 3 {
		t.Fatalf("frames = %d, want 3", len(res.Frames))
	}
	for i, want := range []string{"L", "L", "R"} {
		if got := res.Frames[i].Filter; got != want {
			t.Errorf("frame %d filter = %q, want %q", i, got, want)
		}
	}
}
