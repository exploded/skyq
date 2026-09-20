package phd2

import (
	"math"
	"os"
	"testing"
	"time"
)

// testdata/phd2-guidelog.txt is a hand-built log in the real format: two
// guiding sessions with a calibration block between them, one dropped
// frame, a second session whose header omits the pixel scale, and no
// closing "Guiding Ends" — the shape a log has when PHD2 is still running
// or crashed.
func parseFixture(t *testing.T) *Result {
	t.Helper()
	f, err := os.Open("../../testdata/phd2-guidelog.txt")
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

func at(hh, mm, ss int) time.Time {
	return time.Date(2026, 9, 2, hh, mm, ss, 0, time.UTC)
}

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestParseSessions(t *testing.T) {
	res := parseFixture(t)
	if len(res.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(res.Sessions))
	}
	s0, s1 := res.Sessions[0], res.Sessions[1]
	if !s0.Start.Equal(at(21, 0, 0)) || !s0.End.Equal(at(21, 1, 0)) {
		t.Errorf("session 0 = %s–%s, want 21:00:00–21:01:00", s0.Start, s0.End)
	}
	if s0.Frames != 3 || s0.Dropped != 1 {
		t.Errorf("session 0 frames/dropped = %d/%d, want 3/1", s0.Frames, s0.Dropped)
	}
	// The second header omits "Pixel scale"; the file's last known scale
	// carries forward rather than dropping the whole session.
	near(t, "session 1 pixel scale", s1.PixelScale, 2.0)
	// No "Guiding Ends": the session closes at its own last sample, never
	// at the end of the night.
	if !s1.End.Equal(at(21, 10, 30)) {
		t.Errorf("unterminated session ends %s, want 21:10:30", s1.End)
	}
	if got, want := res.Guiding(), 90*time.Second; got != want {
		t.Errorf("guiding duration = %s, want %s", got, want)
	}
	if got := res.Dropped(); got != 1 {
		t.Errorf("dropped = %d, want 1", got)
	}
}

// A dropped frame carries no distances. Counting it as zero error would
// make a night of lost stars read as perfect guiding.
func TestDroppedFrameIsNotZeroError(t *testing.T) {
	res := parseFixture(t)
	if len(res.Samples) != 5 {
		t.Fatalf("samples = %d, want 5", len(res.Samples))
	}
	for _, s := range res.Samples {
		if s.At.Equal(at(21, 0, 20)) {
			t.Fatal("the DROP row became a sample")
		}
	}
}

// Distances are pixels in the log and arcseconds in the report; the
// session's own pixel scale is the only thing that converts them.
func TestSamplesAreArcsec(t *testing.T) {
	res := parseFixture(t)
	want := []struct {
		At      time.Time
		RA, Dec float64
	}{
		{at(21, 0, 0), 1.0, 0.0},
		{at(21, 0, 10), -1.0, 0.8},
		{at(21, 0, 30), 1.0, -0.8},
		{at(21, 10, 0), 2.0, 2.0},
		{at(21, 10, 30), -2.0, -2.0},
	}
	for i, w := range want {
		got := res.Samples[i]
		if !got.At.Equal(w.At) {
			t.Errorf("sample %d at %s, want %s", i, got.At, w.At)
		}
		near(t, "RA", got.RA, w.RA)
		near(t, "Dec", got.Dec, w.Dec)
	}
}

// Calibration writes its own CSV block. Its rows are steps of a deliberate
// slew, not tracking error, and must never reach the chart.
func TestCalibrationRowsIgnored(t *testing.T) {
	res := parseFixture(t)
	for _, s := range res.Samples {
		if !s.At.Before(at(21, 2, 0)) && s.At.Before(at(21, 3, 0)) {
			t.Fatalf("calibration row became a sample at %s", s.At)
		}
	}
}

// One guide log can span two nights: PHD2 writes to the same file until it
// restarts, so the night's window decides what belongs to the night.
func TestClipToWindow(t *testing.T) {
	res := parseFixture(t).Clip(at(21, 5, 0), at(22, 0, 0))
	if len(res.Samples) != 2 {
		t.Errorf("clipped samples = %d, want 2", len(res.Samples))
	}
	if len(res.Sessions) != 1 {
		t.Errorf("clipped sessions = %d, want 1", len(res.Sessions))
	}
}

func TestFindNightLogs(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"PHD2_GuideLog_2026-09-02_170000.txt", // the night's own log
		"PHD2_GuideLog_2026-09-02_090000.txt", // opened in the morning, still writing at dusk
		"PHD2_GuideLog_2026-08-30_190000.txt", // an earlier night
		"PHD2_DebugLog_2026-09-02_170000.txt", // a different format entirely
		"notes.txt",
	} {
		if err := os.WriteFile(dir+"/"+name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	logs, err := FindNightLogs(dir, start, start.Add(24*time.Hour), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 {
		t.Fatalf("found %d logs, want 2: %v", len(logs), logs)
	}
}
