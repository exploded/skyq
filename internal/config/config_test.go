package config

import "testing"

func TestSkycam(t *testing.T) {
	for _, c := range []struct {
		cfg  Config
		want string
	}{
		{Config{AllSkyBaseURL: "http://allsky.local/images"}, "http://allsky.local"},
		{Config{AllSkyBaseURL: "http://allsky.local:8080/images/"}, "http://allsky.local:8080"},
		{Config{AllSkyBaseURL: "http://allsky.local/images", SkycamURL: "http://10.0.0.5/"}, "http://10.0.0.5"},
		{Config{}, ""},
		{Config{AllSkyBaseURL: "images"}, ""},
	} {
		if got := c.cfg.Skycam(); got != c.want {
			t.Errorf("%+v: Skycam() = %q, want %q", c.cfg, got, c.want)
		}
	}
}
