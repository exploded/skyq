// Package alpaca exposes the live engine as an ASCOM Alpaca
// ObservingConditions device — advisory weather data only, never a
// SafetyMonitor (SPEC §8: transparency must never gate the roof).
//
// There is deliberately no UDP discovery responder: alpaca-switch on this
// machine owns port 32227 and log.Fatalfs if the bind fails. N.I.N.A. adds
// the device manually by address, which probes the management API, so that
// is implemented even without discovery.
//
// Unknown ≠ unsafe: a stale or twilight reading answers with an Alpaca
// error (ValueNotSet, 1026), which clients display as unavailable. It is
// never reported as zero cloud.
package alpaca

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/exploded/skyq/internal/live"
)

// Alpaca error numbers (ASCOM spec).
const (
	errNotImplemented = 0x400 // 1024
	errInvalidValue   = 0x401 // 1025
	errValueNotSet    = 0x402 // 1026
)

const (
	deviceName = "skyq sky transparency"
	deviceDesc = "All-sky cloud volatility and transparency index (advisory; never a safety monitor)"
	driverInfo = "skyq — sky transparency analyser, github.com/exploded/skyq"
	driverVer  = "1.0"
	uniqueID   = "b1946ac9-2be6-4f4e-9d20-5e5b253dd7e0"
)

// Server implements the Alpaca HTTP API over a live.Engine.
type Server struct {
	engine    *live.Engine
	txn       atomic.Uint32
	connected atomic.Bool
}

func New(engine *live.Engine) *Server { return &Server{engine: engine} }

// Handler returns the routes: device API + management API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/management/apiversions", func(w http.ResponseWriter, r *http.Request) {
		s.reply(w, r, []int{1}, 0, "")
	})
	mux.HandleFunc("/management/v1/description", func(w http.ResponseWriter, r *http.Request) {
		s.reply(w, r, map[string]string{
			"ServerName": "skyq", "Manufacturer": "exploded",
			"ManufacturerVersion": driverVer, "Location": "observatory NUC",
		}, 0, "")
	})
	mux.HandleFunc("/management/v1/configureddevices", func(w http.ResponseWriter, r *http.Request) {
		s.reply(w, r, []map[string]any{{
			"DeviceName": deviceName, "DeviceType": "ObservingConditions",
			"DeviceNumber": 0, "UniqueID": uniqueID,
		}}, 0, "")
	})
	mux.HandleFunc("/api/v1/observingconditions/0/", s.device)
	return mux
}

func (s *Server) device(w http.ResponseWriter, r *http.Request) {
	prop := strings.ToLower(strings.TrimPrefix(r.URL.Path, "/api/v1/observingconditions/0/"))
	if prop == "" || strings.Contains(prop, "/") {
		http.Error(w, "unknown device endpoint", http.StatusBadRequest)
		return
	}
	snap := s.engine.Snapshot()

	if r.Method == http.MethodPut {
		s.put(w, r, prop)
		return
	}

	switch prop {
	case "connected":
		s.reply(w, r, s.connected.Load(), 0, "")
	case "name":
		s.reply(w, r, deviceName, 0, "")
	case "description":
		s.reply(w, r, deviceDesc, 0, "")
	case "driverinfo":
		s.reply(w, r, driverInfo, 0, "")
	case "driverversion":
		s.reply(w, r, driverVer, 0, "")
	case "interfaceversion":
		s.reply(w, r, 1, 0, "")
	case "supportedactions":
		s.reply(w, r, []string{}, 0, "")
	case "averageperiod":
		s.reply(w, r, 0.0, 0, "")

	case "cloudcover":
		switch {
		case snap.Cloud == live.StateUnknown:
			s.reply(w, r, nil, errValueNotSet, "cloud cover unavailable: "+snap.Reason)
		default:
			s.reply(w, r, math.Min(100, 100*snap.Volatility/snap.Threshold), 0, "")
		}
	case "skybrightness":
		if snap.Stale || math.IsNaN(snap.Luminance) {
			s.reply(w, r, nil, errValueNotSet, "sky brightness unavailable: no recent still")
		} else {
			// Instrumental units (mean luminance 0–255 of an auto-exposed
			// camera), NOT lux — documented in sensordescription.
			s.reply(w, r, snap.Luminance, 0, "")
		}
	case "skyquality":
		if !snap.IndexOK {
			s.reply(w, r, nil, errValueNotSet, "no live transparency index (no light frames yet)")
		} else {
			// Transparency index, 100 = clear-sky median. NOT mag/arcsec².
			s.reply(w, r, snap.Index, 0, "")
		}
	case "timesincelastupdate":
		if snap.LastStillAt.IsZero() {
			s.reply(w, r, nil, errValueNotSet, "no stills received yet")
		} else {
			s.reply(w, r, time.Since(snap.LastStillAt).Seconds(), 0, "")
		}
	case "sensordescription":
		s.reply(w, r, s.sensorDescription(param(r, "SensorName")), 0, "")

	case "dewpoint", "humidity", "pressure", "rainrate",
		"starfwhm", "temperature", "winddirection", "windgust", "windspeed":
		s.reply(w, r, nil, errNotImplemented, "sensor not implemented: "+prop)

	default:
		http.Error(w, "unknown property "+prop, http.StatusBadRequest)
	}
}

func (s *Server) put(w http.ResponseWriter, r *http.Request, prop string) {
	switch prop {
	case "connected":
		v := strings.ToLower(param(r, "Connected"))
		if v != "true" && v != "false" {
			s.reply(w, r, nil, errInvalidValue, "Connected must be true or false")
			return
		}
		s.connected.Store(v == "true")
		s.reply(w, r, nil, 0, "")
	case "averageperiod":
		if v, err := strconv.ParseFloat(param(r, "AveragePeriod"), 64); err != nil || v != 0 {
			s.reply(w, r, nil, errInvalidValue, "only AveragePeriod 0 is supported")
			return
		}
		s.reply(w, r, nil, 0, "")
	case "refresh":
		s.engine.Refresh()
		s.reply(w, r, nil, 0, "")
	default:
		s.reply(w, r, nil, errNotImplemented, "cannot set "+prop)
	}
}

func (s *Server) sensorDescription(sensor string) string {
	switch strings.ToLower(sensor) {
	case "cloudcover":
		return "Volatility of all-sky luminance against the validated cloud threshold; 100 = at/over threshold. Advisory."
	case "skybrightness":
		return "Mean luminance (0-255) of the upper sky from the auto-exposed all-sky camera. Instrumental units, not lux."
	case "skyquality":
		return "skyq transparency index: median of recent light frames as % of clear-sky baseline. Not mag/arcsec^2."
	default:
		return "not implemented"
	}
}

// param fetches a query/form parameter case-insensitively — the Alpaca
// spec requires clients' parameter casing to be accepted as sent.
func param(r *http.Request, name string) string {
	r.ParseForm()
	for k, v := range r.Form {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// envelope is the standard Alpaca response. Value must NOT be omitempty:
// a legitimate false/0 value (Connected=false, AveragePeriod=0) would
// vanish from the JSON.
type envelope struct {
	Value               any    `json:"Value"`
	ClientTransactionID uint32 `json:"ClientTransactionID"`
	ServerTransactionID uint32 `json:"ServerTransactionID"`
	ErrorNumber         int    `json:"ErrorNumber"`
	ErrorMessage        string `json:"ErrorMessage"`
}

func (s *Server) reply(w http.ResponseWriter, r *http.Request, value any, errNum int, errMsg string) {
	ctid, _ := strconv.ParseUint(param(r, "ClientTransactionID"), 10, 32)
	env := envelope{
		Value:               value,
		ClientTransactionID: uint32(ctid),
		ServerTransactionID: s.txn.Add(1),
		ErrorNumber:         errNum,
		ErrorMessage:        errMsg,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(env); err != nil {
		fmt.Println("alpaca: encoding response:", err)
	}
}
