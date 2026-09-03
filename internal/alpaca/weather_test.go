package alpaca

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/exploded/skyq/internal/live"
	"github.com/exploded/skyq/internal/owm"
)

type stubWeather struct {
	r   owm.Reading
	err error
}

func (s stubWeather) Current() (owm.Reading, error) { return s.r, s.err }

func newWeatherServer(t *testing.T, w WeatherSource) *httptest.Server {
	t.Helper()
	engine := live.New(live.Options{
		CacheDir: t.TempDir(), Loc: time.UTC, Window: 5, Threshold: 6,
		MaxStillAge: 5 * time.Minute, Poll: time.Minute,
	})
	dev := New(engine)
	dev.Weather = w
	srv := httptest.NewServer(dev.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestAmbientSensorsPassThrough(t *testing.T) {
	srv := newWeatherServer(t, stubWeather{r: owm.Reading{
		Temperature: 10.83, Humidity: 77, Pressure: 1011.56, DewPoint: 6.96,
		WindSpeed: 1.11, WindDir: 341, WindGust: 1.72, RainRate: 0,
		At: time.Now(),
	}})
	want := map[string]float64{
		"temperature": 10.83, "humidity": 77, "pressure": 1011.56,
		"dewpoint": 6.96, "windspeed": 1.11, "winddirection": 341,
		"windgust": 1.72, "rainrate": 0,
	}
	for prop, v := range want {
		env := get(t, srv.URL+"/api/v1/observingconditions/0/"+prop)
		if env.ErrorNumber != 0 {
			t.Errorf("%s: ErrorNumber=%d %s", prop, env.ErrorNumber, env.ErrorMessage)
			continue
		}
		// RainRate 0 is a real value (no rain) and must serialise, not vanish.
		got, ok := env.Value.(float64)
		if !ok || got != v {
			t.Errorf("%s = %v, want %v", prop, env.Value, v)
		}
	}
}

func TestAmbientUnavailableIsError(t *testing.T) {
	// A stale/failed weather source answers 1026 — never the last number
	// presented as current (SPEC §8: unknown ≠ known).
	srv := newWeatherServer(t, stubWeather{err: errors.New("weather data 45m0s old")})
	for _, prop := range []string{"temperature", "dewpoint", "rainrate"} {
		env := get(t, srv.URL+"/api/v1/observingconditions/0/"+prop)
		if env.ErrorNumber != errValueNotSet {
			t.Errorf("%s: ErrorNumber=%d, want %d", prop, env.ErrorNumber, errValueNotSet)
		}
	}
}

func TestTimeSinceLastUpdatePerSensor(t *testing.T) {
	at := time.Now().Add(-5 * time.Minute)
	srv := newWeatherServer(t, stubWeather{r: owm.Reading{Temperature: 10, At: at}})
	// Weather sensor: age of the OWM reading (~300 s), case-insensitive name.
	env := get(t, srv.URL+"/api/v1/observingconditions/0/timesincelastupdate?sensorname=Temperature")
	if env.ErrorNumber != 0 {
		t.Fatalf("timesincelastupdate(Temperature): %+v", env)
	}
	if secs, ok := env.Value.(float64); !ok || secs < 295 || secs > 310 {
		t.Errorf("weather age = %v, want ~300s", env.Value)
	}
	// No sensor name: still the stills-based answer (none yet → 1026).
	env = get(t, srv.URL+"/api/v1/observingconditions/0/timesincelastupdate")
	if env.ErrorNumber != errValueNotSet {
		t.Errorf("device-wide: ErrorNumber=%d, want %d", env.ErrorNumber, errValueNotSet)
	}
}

func TestAmbientSensorDescription(t *testing.T) {
	srv := newWeatherServer(t, stubWeather{})
	env := get(t, srv.URL+"/api/v1/observingconditions/0/sensordescription?SensorName=Temperature")
	if env.ErrorNumber != 0 {
		t.Fatalf("sensordescription: %+v", env)
	}
	if s, _ := env.Value.(string); s == "not implemented" || s == "" {
		t.Errorf("Temperature description = %q, want the OpenWeatherMap note", s)
	}
}
