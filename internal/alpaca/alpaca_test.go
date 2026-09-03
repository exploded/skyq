package alpaca

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/exploded/skyq/internal/live"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	// A freshly created engine reports unknown ("starting up") — exactly
	// the state the error-path assertions need.
	engine := live.New(live.Options{
		CacheDir: t.TempDir(), Loc: time.UTC, Window: 5, Threshold: 6,
		MaxStillAge: 5 * time.Minute, Poll: time.Minute,
	})
	srv := httptest.NewServer(New(engine).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) envelope {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s", url, resp.Status)
	}
	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestManagementAPI(t *testing.T) {
	srv := newTestServer(t)
	env := get(t, srv.URL+"/management/v1/configureddevices")
	devices, ok := env.Value.([]any)
	if !ok || len(devices) != 1 {
		t.Fatalf("configureddevices = %v", env.Value)
	}
	d := devices[0].(map[string]any)
	if d["DeviceType"] != "ObservingConditions" {
		t.Errorf("DeviceType = %v — must NEVER be SafetyMonitor", d["DeviceType"])
	}
	if get(t, srv.URL+"/management/apiversions").ErrorNumber != 0 {
		t.Error("apiversions errored")
	}
}

func TestEnvelopeAndCaseInsensitiveParams(t *testing.T) {
	srv := newTestServer(t)
	// Alpaca requires accepting parameter names as the client cases them.
	env := get(t, srv.URL+"/api/v1/observingconditions/0/name?clienttransactionid=77")
	if env.ClientTransactionID != 77 {
		t.Errorf("ClientTransactionID echo = %d, want 77", env.ClientTransactionID)
	}
	if env.ServerTransactionID == 0 {
		t.Error("ServerTransactionID must be non-zero")
	}
	if env.ErrorNumber != 0 || env.Value == "" {
		t.Errorf("name: %+v", env)
	}
}

func TestUnknownIsErrorNeverZero(t *testing.T) {
	srv := newTestServer(t)
	// SPEC §8 rule 3: unknown ≠ unsafe. With no data at all, CloudCover
	// must be an Alpaca error, never a numeric 0 ("clear").
	env := get(t, srv.URL+"/api/v1/observingconditions/0/cloudcover")
	if env.ErrorNumber != errValueNotSet {
		t.Fatalf("cloudcover with no data: ErrorNumber=%d value=%v, want %d", env.ErrorNumber, env.Value, errValueNotSet)
	}
	if env.Value != nil {
		t.Errorf("cloudcover error must not carry a value, got %v", env.Value)
	}
	for _, p := range []string{"skybrightness", "skyquality", "timesincelastupdate"} {
		if e := get(t, srv.URL+"/api/v1/observingconditions/0/"+p); e.ErrorNumber != errValueNotSet {
			t.Errorf("%s with no data: ErrorNumber=%d, want %d", p, e.ErrorNumber, errValueNotSet)
		}
	}
}

func TestNotImplementedSensors(t *testing.T) {
	srv := newTestServer(t)
	for _, p := range []string{"temperature", "humidity", "windspeed", "rainrate"} {
		if e := get(t, srv.URL+"/api/v1/observingconditions/0/"+p); e.ErrorNumber != errNotImplemented {
			t.Errorf("%s: ErrorNumber=%d, want %d", p, e.ErrorNumber, errNotImplemented)
		}
	}
}

// putForm sends an Alpaca PUT (form-encoded body, as the spec requires).
func putForm(t *testing.T, u string, vals url.Values) envelope {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, u, strings.NewReader(vals.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestConnectedRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	// Connected=false must serialise as Value:false, not vanish.
	env := get(t, srv.URL+"/api/v1/observingconditions/0/connected")
	if v, ok := env.Value.(bool); !ok || v {
		t.Fatalf("initial connected = %v, want false", env.Value)
	}
	if e := putForm(t, srv.URL+"/api/v1/observingconditions/0/connected",
		url.Values{"Connected": {"True"}, "ClientTransactionID": {"5"}}); e.ErrorNumber != 0 {
		t.Fatalf("PUT connected: %+v", e)
	}
	env = get(t, srv.URL+"/api/v1/observingconditions/0/connected")
	if v, ok := env.Value.(bool); !ok || !v {
		t.Errorf("after PUT connected = %v, want true", env.Value)
	}
}

func TestAveragePeriodOnlyZero(t *testing.T) {
	srv := newTestServer(t)
	env := putForm(t, srv.URL+"/api/v1/observingconditions/0/averageperiod",
		url.Values{"AveragePeriod": {"5"}})
	if env.ErrorNumber != errInvalidValue {
		t.Errorf("AveragePeriod=5: ErrorNumber=%d, want %d", env.ErrorNumber, errInvalidValue)
	}
}

func TestUnknownPropertyIs400(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/api/v1/observingconditions/0/roofcontrol")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown property status = %d, want 400", resp.StatusCode)
	}
}

func TestNoSafetyMonitorAnywhere(t *testing.T) {
	// Belt and braces on SPEC §8 rule 1: the device must not answer on a
	// safetymonitor path at all.
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/api/v1/safetymonitor/0/issafe")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a safetymonitor endpoint answered — transparency must never gate the roof")
	}
	if !strings.Contains(deviceDesc, "never a safety monitor") {
		t.Error("device description should state the advisory-only stance")
	}
}
