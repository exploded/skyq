package allsky

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func encodeGrey(t *testing.T, w, h int, level uint8) *bytes.Reader {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = level
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestLuminanceUniform(t *testing.T) {
	for _, level := range []uint8{0, 128, 255} {
		got, err := Luminance(encodeGrey(t, 640, 480, level), DefaultBandTop, DefaultBandBottom)
		if err != nil {
			t.Fatal(err)
		}
		if diff := got - float64(level); diff > 2 || diff < -2 {
			t.Errorf("uniform %d image → luminance %.2f", level, got)
		}
	}
}

func TestLuminanceBandSelects(t *testing.T) {
	// Bright band where the crop is, black elsewhere: the crop must see
	// only the band.
	img := image.NewGray(image.Rect(0, 0, 200, 100))
	for y := 6; y < 30; y++ {
		for x := 0; x < 200; x++ {
			img.SetGray(x, y, color.Gray{Y: 200})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	got, err := Luminance(bytes.NewReader(buf.Bytes()), 0.06, 0.30)
	if err != nil {
		t.Fatal(err)
	}
	if got < 180 {
		t.Errorf("band luminance %.1f, want ≈200 (crop leaked outside the band)", got)
	}
}

// TestLuminanceRealStill runs against the one real still in testdata —
// 23:24 on the validated night, clear sky with moonlight. The clear-sky
// baseline that night was 25–30; allow slack for the different code path
// but fail if the number is wildly off.
func TestLuminanceRealStill(t *testing.T) {
	f, err := os.Open("../../testdata/stills/image-20260902232414.jpg")
	if err != nil {
		t.Skip("real still not present")
	}
	defer f.Close()
	got, err := Luminance(f, DefaultBandTop, DefaultBandBottom)
	if err != nil {
		t.Fatal(err)
	}
	if got < 15 || got > 45 {
		t.Errorf("real clear-sky still luminance = %.2f, expected roughly 25–30", got)
	}
	t.Logf("real still luminance: %.2f", got)
}

// TestLuminanceRealCloudPeak: the 05:19 still is the night's moonlit-cloud
// peak — the prototype measured 88.9 there. The moonlit cloud must read far
// brighter than the clear sky.
func TestLuminanceRealCloudPeak(t *testing.T) {
	f, err := os.Open("../../testdata/stills/image-20260903051912.jpg")
	if err != nil {
		t.Skip("real still not present")
	}
	defer f.Close()
	got, err := Luminance(f, DefaultBandTop, DefaultBandBottom)
	if err != nil {
		t.Fatal(err)
	}
	if got < 60 {
		t.Errorf("peak-cloud still luminance = %.2f, expected well above the 25–30 clear baseline", got)
	}
	t.Logf("peak-cloud still luminance: %.2f (prototype: 88.9)", got)
}

func TestParseStillTime(t *testing.T) {
	at, ok := ParseStillTime("image-20260902232414.jpg", time.UTC)
	if !ok {
		t.Fatal("did not parse")
	}
	want := time.Date(2026, 9, 2, 23, 24, 14, 0, time.UTC)
	if !at.Equal(want) {
		t.Errorf("got %v, want %v", at, want)
	}
	for _, bad := range []string{"keogram-20260902.jpg", "image-2026090223241.jpg", "image-20260902232414.png"} {
		if _, ok := ParseStillTime(bad, time.UTC); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestFetcherSync(t *testing.T) {
	still := encodeGrey(t, 8, 8, 100)
	stillBytes := make([]byte, still.Len())
	still.Read(stillBytes)

	var authed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "james" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authed = true
		switch r.URL.Path {
		case "/images/20260902/":
			w.Write([]byte(`<html><a href="image-20260902232414.jpg">image-20260902232414.jpg</a>` +
				`<a href="image-20260902232514.jpg">image-20260902232514.jpg</a>` +
				`<a href="image-20260902232614.jpg">image-20260902232614.jpg</a>` +
				`<a href="keogram-20260902.jpg">keogram</a></html>`))
		case "/images/20260902/image-20260902232414.jpg", "/images/20260902/image-20260902232514.jpg":
			w.Write(stillBytes)
		case "/images/20260902/image-20260902232614.jpg":
			// A mid-write or permission-broken file on the Pi: one bad
			// still must not abort the rest of the night's sync.
			w.WriteHeader(http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cache := t.TempDir()
	f := &Fetcher{BaseURL: srv.URL + "/images", Username: "james", Password: "secret", CacheDir: cache}

	fetched, err := f.Sync(context.Background(), "20260902")
	if err == nil || !strings.Contains(err.Error(), "1 of 3 stills failed") {
		t.Fatalf("want a 1-of-3 skip error, got %v", err)
	}
	if len(fetched) != 2 {
		t.Fatalf("fetched %d files, want 2 despite the forbidden one: %v", len(fetched), fetched)
	}
	if !authed {
		t.Error("server never saw valid basic auth")
	}
	// Second sync re-attempts only the failed file.
	fetched, err = f.Sync(context.Background(), "20260902")
	if err == nil {
		t.Error("still-forbidden file should still report an error")
	}
	if len(fetched) != 0 {
		t.Errorf("re-sync fetched %d files, want 0", len(fetched))
	}
	stills, err := ScanDir(filepath.Join(cache, "20260902"), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(stills) != 2 {
		t.Errorf("cache holds %d stills, want 2", len(stills))
	}
}
