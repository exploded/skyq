package config

import (
	"os"
	"path/filepath"
	"testing"
)

func load(t *testing.T, url string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"allsky_base_url": "`+url+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestAllSkyBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://allsky.local":      "http://allsky.local",
		"http://allsky.local/":     "http://allsky.local",
		"http://allsky.local:8080": "http://allsky.local:8080",
		"":                         "",
	} {
		cfg, err := load(t, in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if cfg.AllSkyBaseURL != want {
			t.Errorf("%q loaded as %q, want %q", in, cfg.AllSkyBaseURL, want)
		}
	}
	// The pre-skycam value would 404 every sky API call into "cloud unknown".
	for _, bad := range []string{"http://allsky.local/images", "allsky.local", "/images"} {
		if _, err := load(t, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
