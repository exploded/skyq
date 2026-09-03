// Package config loads skyq's JSON config. The config file holds the
// AllSky credentials, so it is gitignored; config.example.json ships in the
// repo instead. All behaviour changes go through this file plus the CLI —
// there is no settings UI, by design.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type Publish struct {
	Enabled bool   `json:"enabled"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	User    string `json:"user"`
	Dest    string `json:"dest"` // remote dir, e.g. /var/www/deepspaceplace/reports
	// IdentityFile is the OpenSSH private key used for scp. Empty falls
	// back to the default keys / ~/.ssh/config.
	IdentityFile string `json:"identity_file"`
}

type Config struct {
	// Timezone the observatory logs and filenames are written in. Empty
	// means the machine's local zone (right for the NUC).
	Timezone string `json:"timezone"`

	NINALogDir string `json:"nina_log_dir"` // %LOCALAPPDATA%\NINA\Logs

	AllSkyBaseURL  string `json:"allsky_base_url"` // http://allsky.local/images
	AllSkyUsername string `json:"allsky_username"`
	AllSkyPassword string `json:"allsky_password"`

	CacheDir   string `json:"cache_dir"`
	DBPath     string `json:"db_path"`
	ReportsDir string `json:"reports_dir"`

	BandTop             float64 `json:"band_top"`
	BandBottom          float64 `json:"band_bottom"`
	VolatilityWindow    int     `json:"volatility_window"`
	VolatilityThreshold float64 `json:"volatility_threshold"`

	// Phase 2 (skyq serve). Latitude/longitude drive the astronomical
	// darkness gate — cloud volatility only means something once nautical
	// twilight has ended. The Alpaca API stays on loopback: N.I.N.A. is on
	// this machine, and loopback needs no firewall rule. No UDP discovery —
	// alpaca-switch owns port 32227 on this box.
	Latitude       float64 `json:"latitude"`
	Longitude      float64 `json:"longitude"`
	AlpacaAddr     string  `json:"alpaca_addr"`
	LiveAddr       string  `json:"live_addr"`
	PollSeconds    int     `json:"poll_seconds"`
	MaxStillAgeMin int     `json:"max_still_age_minutes"`

	// OpenWeatherMapAPIKey enables the ambient-conditions pass-through
	// (temperature, humidity, pressure, dew point, wind, rain) on the
	// Alpaca device, so N.I.N.A.'s FITS headers lose nothing when its
	// weather source switches from OpenWeatherMap to skyq. Empty leaves
	// those sensors not-implemented.
	OpenWeatherMapAPIKey string `json:"openweathermap_api_key"`

	Publish Publish `json:"publish"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		CacheDir:            "cache",
		DBPath:              "skyq.db",
		ReportsDir:          "reports",
		BandTop:             0.06,
		BandBottom:          0.30,
		VolatilityWindow:    10,
		VolatilityThreshold: 6.0,
		AlpacaAddr:          "127.0.0.1:11112",
		LiveAddr:            ":8996",
		PollSeconds:         60,
		MaxStillAgeMin:      5,
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.NINALogDir == "" {
		cfg.NINALogDir = os.ExpandEnv(`${LOCALAPPDATA}\NINA\Logs`)
	}
	return cfg, nil
}

// Location resolves the configured timezone.
func (c *Config) Location() (*time.Location, error) {
	if c.Timezone == "" {
		return time.Local, nil
	}
	return time.LoadLocation(c.Timezone)
}
