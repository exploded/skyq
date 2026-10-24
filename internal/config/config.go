// Package config loads skyq's JSON config. The config file holds the
// skycam credentials, so it is gitignored; config.example.json ships in the
// repo instead. All behaviour changes go through this file plus the CLI —
// there is no settings UI, by design.
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
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

	// PHD2LogDir holds the PHD2 guide logs (PHD2_GuideLog_*.txt). Empty
	// means PHD2's own default, %USERPROFILE%\Documents\PHD2. Guiding is a
	// bonus input: a directory that does not exist costs the report its
	// guiding card and nothing else.
	PHD2LogDir string `json:"phd2_log_dir"`

	// AllSkyBaseURL is the Pi's stills folder, http://allsky.local/images.
	// skycam serves it in allsky's layout, and the report's thumbnails come
	// from it. The credentials cover the sky API as well.
	AllSkyBaseURL  string `json:"allsky_base_url"`
	AllSkyUsername string `json:"allsky_username"`
	AllSkyPassword string `json:"allsky_password"`

	// SkycamURL is skycam's web server on the Pi, which serves the sky API
	// (cloud, onset and roof per night) and /images/. Empty means the scheme
	// and host of AllSkyBaseURL, which is where skycam runs.
	SkycamURL string `json:"skycam_url"`

	CacheDir   string `json:"cache_dir"`
	DBPath     string `json:"db_path"`
	ReportsDir string `json:"reports_dir"`

	Publish Publish `json:"publish"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		CacheDir:   "cache",
		DBPath:     "skyq.db",
		ReportsDir: "reports",
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.NINALogDir == "" {
		cfg.NINALogDir = os.ExpandEnv(`${LOCALAPPDATA}\NINA\Logs`)
	}
	if cfg.PHD2LogDir == "" {
		cfg.PHD2LogDir = os.ExpandEnv(`${USERPROFILE}\Documents\PHD2`)
	}
	return cfg, nil
}

// Skycam returns the skycam base URL: SkycamURL, or failing that the scheme
// and host of AllSkyBaseURL. Empty when neither is set.
func (c *Config) Skycam() string {
	if c.SkycamURL != "" {
		return strings.TrimRight(c.SkycamURL, "/")
	}
	u, err := url.Parse(c.AllSkyBaseURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Location resolves the configured timezone.
func (c *Config) Location() (*time.Location, error) {
	if c.Timezone == "" {
		return time.Local, nil
	}
	return time.LoadLocation(c.Timezone)
}
