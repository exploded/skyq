package phd2

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestParseRealGuideLogs runs the parser over every real PHD2 guide log in
// .local/PHD2 (not committed; the test skips when absent). It asserts
// nothing about content — only that real logs parse without error and
// without impossible state.
func TestParseRealGuideLogs(t *testing.T) {
	dir := "../../.local/PHD2"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skip("no .local/PHD2; skipping robustness sweep")
	}
	for _, e := range entries {
		if e.IsDir() || !reGuideLogName.MatchString(e.Name()) {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			f, err := os.Open(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			res, err := Parse(f, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Samples) == 0 {
				t.Fatal("no guide samples parsed from a real log")
			}
			for _, s := range res.Samples {
				if s.At.IsZero() {
					t.Fatal("sample with zero time")
				}
				// A real rig guides inside a few arcseconds. Anything past a
				// degree means the pixel scale or the columns were misread.
				if s.Total() > 3600 {
					t.Fatalf("impossible guide error %.1f arcsec at %s", s.Total(), s.At)
				}
			}
			for _, ss := range res.Sessions {
				if ss.End.Before(ss.Start) {
					t.Fatalf("session ends before it starts: %+v", ss)
				}
				if ss.PixelScale <= 0 && ss.Frames > 0 {
					t.Fatalf("session has frames but no pixel scale: %+v", ss)
				}
			}
			// Samples must come out in time order for the binner.
			for i := 1; i < len(res.Samples); i++ {
				if res.Samples[i].At.Before(res.Samples[i-1].At) {
					t.Fatalf("samples out of order at %d", i)
				}
			}
		})
	}
}
