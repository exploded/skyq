package main

import (
	"context"
	"database/sql"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/exploded/skyq/internal/analysis"
	"github.com/exploded/skyq/internal/config"
	"github.com/exploded/skyq/internal/store"
	db "github.com/exploded/skyq/internal/store/db"
)

// fakePi stands in for skycam on allsky.local: the hand-built sky fixture
// for 2026-09-02 and the real stills in testdata/stills, behind basic auth.
// Every other still 404s, as one past skycam's 14-night retention does.
// While down is set, every request 404s, as for a night skycam never saw.
func fakePi(t *testing.T, down *atomic.Bool) *httptest.Server {
	t.Helper()
	sky, err := os.ReadFile("testdata/sky-20260902.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "skyq" || p != "secret" {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		switch {
		case down.Load():
			http.NotFound(w, r)
		case r.URL.Path == "/api/nights/20260902/sky":
			w.Write(sky)
		case strings.HasPrefix(r.URL.Path, "/images/20260902/"):
			http.ServeFile(w, r, filepath.Join("testdata/stills", filepath.Base(r.URL.Path)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// testConfig lays out a NUC in a temp dir: the validated log under its real
// N.I.N.A. filename, and fresh cache, database and reports.
func testConfig(t *testing.T, skycamURL string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	logs := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/20260902.log")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "20260902-201750-3.2.0.9001.8256-202609.log"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return &config.Config{
		NINALogDir: logs, SkycamURL: skycamURL,
		AllSkyUsername: "skyq", AllSkyPassword: "secret",
		CacheDir: filepath.Join(dir, "cache"), DBPath: filepath.Join(dir, "skyq.db"),
		ReportsDir: filepath.Join(dir, "reports"),
	}
}

type nightRows struct {
	night  db.Night
	events []db.Event
	frames []db.Frame
	stored []db.Baseline
	html   string
}

func readNight(t *testing.T, cfg *config.Config) nightRows {
	t.Helper()
	sdb, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	q := db.New(sdb)
	ctx := context.Background()
	var out nightRows
	if out.night, err = q.GetNight(ctx, "2026-09-02"); err != nil {
		t.Fatal(err)
	}
	if out.events, err = q.ListEvents(ctx, out.night.ID); err != nil {
		t.Fatal(err)
	}
	if out.frames, err = q.ListFrames(ctx, out.night.ID); err != nil {
		t.Fatal(err)
	}
	if out.stored, err = q.ListBaselines(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(filepath.Join(cfg.ReportsDir, "2026-09-02.html"))
	if err != nil {
		t.Fatal(err)
	}
	out.html = string(page)
	return out
}

// SPEC §9 end to end: the report run takes its onset from skycam's sky API,
// and the validated night's index and attribution come out unchanged. The
// sky reply is hand-built (skycam did not exist that night) around the
// recorded 03:48 onset.
func TestReportFromSkycam(t *testing.T) {
	var down atomic.Bool
	cfg := testConfig(t, fakePi(t, &down).URL)
	if err := runReport(cfg, time.UTC, "2026-09-02"); err != nil {
		t.Fatal(err)
	}
	got := readNight(t, cfg)

	if got.night.CloudSource != "skycam" || got.night.CloudOnsetAt != (sql.NullString{String: "2026-09-03T03:48:00Z", Valid: true}) {
		t.Fatalf("night = %s onset %v, want skycam 03:48", got.night.CloudSource, got.night.CloudOnsetAt)
	}
	checkSection9(t, got)
	for _, want := range []string{"Cloud arrived at 03:48", "All&#8209;sky cloud cover", "most cloud, 97%", `src="data:image/jpeg;base64,`} {
		if !strings.Contains(got.html, want) {
			t.Errorf("report missing %q", want)
		}
	}
	if len(got.stored) == 0 {
		t.Error("a skycam night stored no clear-sky baselines")
	}

	// The Pi forgets the night (or is down) and the night is rerun: the
	// stored verdict stands rather than turning into unknown.
	down.Store(true)
	if err := runReport(cfg, time.UTC, "2026-09-02"); err != nil {
		t.Fatal(err)
	}
	again := readNight(t, cfg)
	if again.night.CloudSource != "skycam" || again.night.CloudOnsetAt != got.night.CloudOnsetAt {
		t.Errorf("rerun night = %s onset %v, want the stored skycam 03:48", again.night.CloudSource, again.night.CloudOnsetAt)
	}
	checkSection9(t, again)
	if !strings.Contains(again.html, "Cloud arrived at 03:48") {
		t.Error("rerun report lost the onset")
	}
}

// checkSection9 is SPEC §9's index and attribution, read back from the
// database the report run wrote.
func checkSection9(t *testing.T, got nightRows) {
	t.Helper()
	onset := "2026-09-03T03:48:00Z"
	var before, after []float64
	for _, f := range got.frames {
		if f.Class != "light" || !f.IndexPct.Valid {
			continue
		}
		if f.At < onset {
			before = append(before, f.IndexPct.Float64)
		} else {
			after = append(after, f.IndexPct.Float64)
		}
	}
	b, a := analysis.Summarise(before), analysis.Summarise(after)
	for _, c := range []struct {
		name      string
		got, want float64
	}{
		{"n before", float64(b.N), 63}, {"median before", b.Median, 100}, {"Q1 before", b.Q1, 86}, {"Q3 before", b.Q3, 107},
		{"n after", float64(a.N), 16}, {"median after", a.Median, 50}, {"Q1 after", a.Q1, 13}, {"Q3 after", a.Q3, 75},
	} {
		if math.Round(c.got) != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	causes := map[string]int{}
	for _, e := range got.events {
		switch e.Kind {
		case "phd2_error":
			want := "cloud"
			if e.At == "2026-09-03T00:17:12Z" { // follows the 00:10:39 slew
				want = "post_slew"
			}
			if e.Cause != want {
				t.Errorf("PHD2 error at %s attributed %s, want %s", e.At, e.Cause, want)
			}
			causes[e.Cause]++
		case "solve_failed":
			if e.Cause != "cloud" {
				t.Errorf("solve failure at %s attributed %s, want cloud", e.At, e.Cause)
			}
		}
	}
	if causes["post_slew"] != 1 || causes["cloud"] != 5 {
		t.Errorf("PHD2 causes = %v, want 1 post_slew, 5 cloud", causes)
	}
}

// A night skycam cannot serve, with nothing on record, has unknown cloud:
// no onset, no failure blamed on cloud, and no clear-sky baselines stored
// for other nights to lean on.
func TestReportCloudUnknown(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	cfg := testConfig(t, fakePi(t, &down).URL)
	if err := runReport(cfg, time.UTC, "2026-09-02"); err != nil {
		t.Fatal(err)
	}
	got := readNight(t, cfg)
	if got.night.CloudSource != "unknown" || got.night.CloudOnsetAt.Valid {
		t.Errorf("night = %s onset %v, want unknown with no onset", got.night.CloudSource, got.night.CloudOnsetAt)
	}
	for _, e := range got.events {
		if e.Cause == "cloud" {
			t.Errorf("%s at %s blamed on cloud with cloud unknown", e.Kind, e.At)
		}
	}
	if len(got.stored) != 0 {
		t.Errorf("unknown night stored baselines %+v", got.stored)
	}
	if !strings.Contains(got.html, "Cloud unknown") || strings.Contains(got.html, "No cloud onset detected") {
		t.Error("report does not say cloud is unknown")
	}
}

// With no skycam configured at all, the report still runs on the log.
func TestReportWithoutSkycam(t *testing.T) {
	cfg := testConfig(t, "")
	if err := runReport(cfg, time.UTC, "2026-09-02"); err != nil {
		t.Fatal(err)
	}
	if got := readNight(t, cfg); got.night.CloudSource != "unknown" {
		t.Errorf("cloud_source = %s, want unknown", got.night.CloudSource)
	}
}
