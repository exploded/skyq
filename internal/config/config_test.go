package config

import (
	"os"
	"path/filepath"
	"testing"
)

func loadJSON(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func load(t *testing.T, url string) (*Config, error) {
	t.Helper()
	return loadJSON(t, `{"skycam_url": "`+url+`"}`)
}

func TestSkycamURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://skycam.local":      "http://skycam.local",
		"http://skycam.local/":     "http://skycam.local",
		"http://skycam.local:8080": "http://skycam.local:8080",
		"":                         "",
	} {
		cfg, err := load(t, in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if cfg.SkycamURL != want {
			t.Errorf("%q loaded as %q, want %q", in, cfg.SkycamURL, want)
		}
	}
	// The pre-skycam value would 404 every sky API call into "cloud unknown".
	for _, bad := range []string{"http://allsky.local/images", "skycam.local", "/images"} {
		if _, err := load(t, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// A config.json written before the rename still works.
func TestLegacyAllskyKeys(t *testing.T) {
	cfg, err := loadJSON(t, `{"allsky_base_url": "http://allsky.local/", "allsky_username": "skyq", "allsky_password": "pw"}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SkycamURL != "http://allsky.local" || cfg.SkycamUsername != "skyq" || cfg.SkycamPassword != "pw" {
		t.Errorf("legacy keys loaded as %q %q %q", cfg.SkycamURL, cfg.SkycamUsername, cfg.SkycamPassword)
	}
	if _, err := loadJSON(t, `{"allsky_base_url": "http://allsky.local/images"}`); err == nil {
		t.Error("legacy URL with a path accepted")
	}
}

// The new keys win when a config has both.
func TestNewKeysWin(t *testing.T) {
	cfg, err := loadJSON(t, `{"skycam_url": "http://skycam.local", "allsky_base_url": "http://allsky.local",
		"skycam_username": "new", "allsky_username": "old"}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SkycamURL != "http://skycam.local" || cfg.SkycamUsername != "new" {
		t.Errorf("got %q %q, want the skycam_* values", cfg.SkycamURL, cfg.SkycamUsername)
	}
}
