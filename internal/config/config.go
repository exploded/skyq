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

	// SkycamURL is skycam's web server on the Pi, http://skycam.local.
	// It serves the sky API (cloud, onset and roof per night) and the
	// stills under /images/, both behind these Basic auth credentials.
	// Empty leaves every night's cloud unknown.
	SkycamURL      string `json:"skycam_url"`
	SkycamUsername string `json:"skycam_username"`
	SkycamPassword string `json:"skycam_password"`

	CacheDir   string `json:"cache_dir"`
	DBPath     string `json:"db_path"`
	ReportsDir string `json:"reports_dir"`

	Publish Publish `json:"publish"`
}

// legacyKeys are the skycam settings' names from before the Pi was renamed
// from allsky to skycam (2026-10-27). They are still read, so a config.json
// on the NUC keeps working until it is updated; the new keys win if both are
// set.
type legacyKeys struct {
	BaseURL  string `json:"allsky_base_url"`
	Username string `json:"allsky_username"`
	Password string `json:"allsky_password"`
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
	var old legacyKeys
	if err := json.Unmarshal(raw, &old); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.SkycamURL == "" {
		cfg.SkycamURL = old.BaseURL
	}
	if cfg.SkycamUsername == "" {
		cfg.SkycamUsername = old.Username
	}
	if cfg.SkycamPassword == "" {
		cfg.SkycamPassword = old.Password
	}
	if cfg.NINALogDir == "" {
		cfg.NINALogDir = os.ExpandEnv(`${LOCALAPPDATA}\NINA\Logs`)
	}
	if cfg.PHD2LogDir == "" {
		cfg.PHD2LogDir = os.ExpandEnv(`${USERPROFILE}\Documents\PHD2`)
	}
	if err := cfg.checkSkycamURL(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// checkSkycamURL rejects a skycam_url with a path. The pre-skycam value,
// http://allsky.local/images, would send every sky API request to a path
// that 404s, and a 404 reads as cloud unknown, so the mistake would show
// only as a run of nights with no cloud.
func (c *Config) checkSkycamURL() error {
	c.SkycamURL = strings.TrimRight(c.SkycamURL, "/")
	if c.SkycamURL == "" {
		return nil
	}
	u, err := url.Parse(c.SkycamURL)
	if err != nil || u.Host == "" || u.Path != "" {
		return fmt.Errorf("skycam_url %q must be the Pi's address alone, e.g. http://skycam.local", c.SkycamURL)
	}
	return nil
}

// Location resolves the configured timezone.
func (c *Config) Location() (*time.Location, error) {
	if c.Timezone == "" {
		return time.Local, nil
	}
	return time.LoadLocation(c.Timezone)
}
