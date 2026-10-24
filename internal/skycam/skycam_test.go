package skycam

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exploded/skyq/internal/ninalog"
)

// fakePi serves the hand-built 2026-09-02 sky fixture and the real stills in
// testdata/stills, behind basic auth, the way skycam does. Every other still
// 404s, like one past skycam's 14-night retention.
func fakePi(t *testing.T) *httptest.Server {
	t.Helper()
	sky, err := os.ReadFile("../../testdata/sky-20260902.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "skyq" || p != "secret" {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/nights/20260902/sky":
			w.Header().Set("Content-Type", "application/json")
			w.Write(sky)
		case strings.HasPrefix(r.URL.Path, "/images/20260902/"):
			http.ServeFile(w, r, filepath.Join("../../testdata/stills", filepath.Base(r.URL.Path)))
		default:
			http.Error(w, "no frames for that night", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func client(srv *httptest.Server, t *testing.T) *Client {
	return &Client{BaseURL: srv.URL, Username: "skyq", Password: "secret", CacheDir: t.TempDir()}
}

func TestNight(t *testing.T) {
	c := client(fakePi(t), t)
	n, err := c.Night(context.Background(), "20260902", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !n.HasOnset || n.Onset.Format("15:04:05") != "03:48:00" {
		t.Errorf("onset = %v (%v), want 03:48:00", n.Onset, n.HasOnset)
	}
	if len(n.Frames) != 295 {
		t.Errorf("frames = %d, want 295", len(n.Frames))
	}
	if f := n.Frames[0]; f.Cloud != nil || f.Roof != RoofOpen {
		t.Errorf("first frame = %+v, want null cloud (baseline warm-up), roof OPEN", f)
	}
}

func TestNightNotFound(t *testing.T) {
	c := client(fakePi(t), t)
	if _, err := c.Night(context.Background(), "20261001", time.UTC); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestNightUnreachable(t *testing.T) {
	srv := fakePi(t)
	c := client(srv, t)
	srv.Close()
	if _, err := c.Night(context.Background(), "20260902", time.UTC); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want a transport error", err)
	}
}

func TestNightWrongPassword(t *testing.T) {
	c := client(fakePi(t), t)
	c.Password = "wrong"
	if _, err := c.Night(context.Background(), "20260902", time.UTC); err == nil {
		t.Error("no error with the wrong password")
	}
}

func TestStill(t *testing.T) {
	c := client(fakePi(t), t)
	ctx := context.Background()
	p, err := c.Still(ctx, "/images/20260902/image-20260903051912.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(c.CacheDir, "20260902", "image-20260903051912.jpg"); p != want {
		t.Errorf("path = %s, want %s", p, want)
	}
	// Cached: a dead Pi still serves it.
	c.BaseURL = "http://127.0.0.1:1"
	if _, err := c.Still(ctx, "/images/20260902/image-20260903051912.jpg"); err != nil {
		t.Errorf("cached still refetched: %v", err)
	}
}

func TestStillGone(t *testing.T) {
	c := client(fakePi(t), t)
	_, err := c.Still(context.Background(), "/images/20260902/image-20260902204000.jpg")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(c.CacheDir, "20260902", "image-20260902204000.jpg.part")); err == nil {
		t.Error("left a .part file behind")
	}
}

func TestStillRejectsOddPaths(t *testing.T) {
	c := client(fakePi(t), t)
	for _, p := range []string{"", "/images/", "/images/../skycam.db", "/images/2026/x.jpg", "/images/20260902/../../x.jpg"} {
		if _, err := c.Still(context.Background(), p); err == nil {
			t.Errorf("Still(%q) accepted", p)
		}
	}
}

func at(hm string) time.Time {
	t, _ := time.Parse("15:04", hm)
	return time.Date(2026, 9, 2, t.Hour(), t.Minute(), 0, 0, time.UTC)
}

func cloud(v float64) *float64 { return &v }

func TestCloudKnown(t *testing.T) {
	n := &Night{Frames: []Frame{
		{At: at("21:00")}, {At: at("21:10"), Cloud: cloud(0.1)}, {At: at("23:00")},
	}}
	if !n.CloudKnown(at("20:00"), at("22:00")) {
		t.Error("21:10 reading not seen")
	}
	if n.CloudKnown(at("22:00"), at("23:30")) {
		t.Error("only null frames, yet cloud known")
	}
	if !n.CloudKnownAt(at("21:14")) || n.CloudKnownAt(at("21:20")) || n.CloudKnownAt(at("23:00")) {
		t.Error("CloudKnownAt reach wrong")
	}
}

func TestRoofSpans(t *testing.T) {
	m := func(hm string, roof string) Frame { return Frame{At: at(hm), Roof: roof} }
	n := &Night{Frames: []Frame{
		m("21:00", RoofClosed), m("21:01", RoofClosed), m("21:02", RoofOpening),
		m("21:03", RoofOpen), m("21:04", RoofOpen), m("21:05", RoofUnknown),
		m("21:06", RoofError), m("21:07", RoofError),
		m("22:00", RoofClosed), m("22:01", RoofClosing),
	}}
	got := n.RoofSpans()
	want := []struct {
		from, to string
		state    ninalog.RoofState
	}{
		{"21:00", "21:02", ninalog.RoofClosed},
		{"21:02", "21:03", ninalog.RoofMoving},
		{"21:03", "21:05", ninalog.RoofOpen},
		// 21:05 unknown: no span. 21:07 → 22:00 is a gap: no span.
		{"21:06", "21:07", ninalog.RoofClosed},
		{"22:00", "22:01", ninalog.RoofClosed},
	}
	if len(got) != len(want) {
		t.Fatalf("spans = %+v, want %d", got, len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.From.Format("15:04") != w.from || g.To.Format("15:04") != w.to || g.State != w.state {
			t.Errorf("span %d = %s–%s %s, want %s–%s %s", i, g.From.Format("15:04"), g.To.Format("15:04"), g.State, w.from, w.to, w.state)
		}
	}
}
