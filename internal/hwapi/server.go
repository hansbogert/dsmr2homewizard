// Package hwapi serves the HomeWizard Energy local API (v1 over HTTP, v2 over HTTPS) for a P1
// meter, backed by DSMR telegrams.
package hwapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const (
	productType = "HWE-P1"
	productName = "P1 Meter"
	firmwareV1  = "5.18"
	firmwareV2  = "6.0200"
	apiV2       = "2.0.0"
)

// Device is one emulated P1 meter, shared by the v1 and v2 servers.
type Device struct {
	State    *State
	Latest   *Latest
	WifiSSID string
	Log      *slog.Logger

	start     time.Time
	pairUntil atomic.Int64 // unix nanos until which POST /api/user is accepted
}

func NewDevice(state *State, latest *Latest, wifiSSID string, log *slog.Logger) *Device {
	return &Device{State: state, Latest: latest, WifiSSID: wifiSSID, Log: log, start: time.Now()}
}

// OpenPairing accepts new v2 users for d, the emulated "press the button on the meter".
func (d *Device) OpenPairing(dur time.Duration) {
	d.pairUntil.Store(time.Now().Add(dur).UnixNano())
	d.Log.Info("v2 pairing window open", "for", dur)
}

func (d *Device) pairingOpen() bool { return time.Now().UnixNano() < d.pairUntil.Load() }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeTelegram(w http.ResponseWriter, raw string) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(raw))
}

// --- API v1 (HTTP, no auth) ---

func v1Error(w http.ResponseWriter, code, id int, desc string) {
	writeJSON(w, code, map[string]any{"error": map[string]any{"id": id, "description": desc}})
}

// V1Handler serves API v1. Data endpoints answer 503 when no fresh telegram is available, so a
// consumer steering a battery sees an outage rather than a frozen or zero reading.
func (d *Device) V1Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"product_type": productType, "product_name": productName,
			"serial": d.State.Serial(), "firmware_version": firmwareV1, "api_version": "v1",
		})
	})
	enabled := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if _, v1 := d.State.settings(); !v1 {
				v1Error(w, 403, 202, "API not enabled")
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /api/v1/data", enabled(func(w http.ResponseWriter, r *http.Request) {
		t := d.Latest.Get()
		if t == nil {
			v1Error(w, 503, 503, "no recent P1 telegram")
			return
		}
		writeJSON(w, 200, fromTelegram(t).v1(d.WifiSSID))
	}))
	mux.HandleFunc("GET /api/v1/telegram", enabled(func(w http.ResponseWriter, r *http.Request) {
		t := d.Latest.Get()
		if t == nil {
			v1Error(w, 503, 503, "no recent P1 telegram")
			return
		}
		writeTelegram(w, t.Raw)
	}))
	mux.HandleFunc("GET /api/v1/system", enabled(func(w http.ResponseWriter, r *http.Request) {
		cloud, _ := d.State.settings()
		writeJSON(w, 200, map[string]any{"cloud_enabled": cloud})
	}))
	mux.HandleFunc("PUT /api/v1/system", enabled(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			CloudEnabled *bool `json:"cloud_enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			v1Error(w, 400, 2, "invalid JSON")
			return
		}
		if err := d.State.update(body.CloudEnabled, nil); err != nil {
			v1Error(w, 500, 500, err.Error())
			return
		}
		cloud, _ := d.State.settings()
		writeJSON(w, 200, map[string]any{"cloud_enabled": cloud})
	}))
	mux.HandleFunc("PUT /api/v1/identify", enabled(func(w http.ResponseWriter, r *http.Request) {
		d.Log.Info("identify requested", "remote", r.RemoteAddr)
		writeJSON(w, 200, map[string]any{"identify": "ok"})
	}))
	return logRequests(d.Log, mux)
}

// --- API v2 (HTTPS, bearer token) ---

func v2Error(w http.ResponseWriter, code int, id string) {
	writeJSON(w, code, map[string]any{"error": id})
}

func (d *Device) V2Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"product_name": productName, "product_type": productType,
			"serial": d.State.Serial(), "firmware_version": firmwareV2, "api_version": apiV2,
		})
	})
	mux.HandleFunc("POST /api/user", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
			!strings.HasPrefix(body.Name, "local/") || len(body.Name) <= len("local/") {
			v2Error(w, 400, "request:invalid")
			return
		}
		if !d.pairingOpen() {
			v2Error(w, 403, "user:creation-not-enabled")
			return
		}
		tok, err := d.State.addUser(body.Name)
		if err != nil {
			v2Error(w, 500, "internal")
			return
		}
		d.Log.Info("v2 user paired", "name", body.Name, "remote", r.RemoteAddr)
		writeJSON(w, 200, map[string]any{"token": tok, "name": body.Name})
	})
	mux.HandleFunc("DELETE /api/user", d.auth(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			v2Error(w, 400, "request:invalid")
			return
		}
		ok, err := d.State.deleteUser(body.Name)
		switch {
		case err != nil:
			v2Error(w, 500, "internal")
		case !ok:
			v2Error(w, 404, "user:not-found")
		default:
			w.WriteHeader(204)
		}
	}))
	mux.HandleFunc("GET /api/measurement", d.auth(func(w http.ResponseWriter, r *http.Request) {
		t := d.Latest.Get()
		if t == nil {
			v2Error(w, 503, "measurement:unavailable")
			return
		}
		writeJSON(w, 200, fromTelegram(t).v2())
	}))
	mux.HandleFunc("GET /api/telegram", d.auth(func(w http.ResponseWriter, r *http.Request) {
		t := d.Latest.Get()
		if t == nil {
			v2Error(w, 503, "measurement:unavailable")
			return
		}
		writeTelegram(w, t.Raw)
	}))
	mux.HandleFunc("GET /api/system", d.auth(d.v2System))
	mux.HandleFunc("PUT /api/system", d.auth(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			CloudEnabled *bool `json:"cloud_enabled"`
			APIv1Enabled *bool `json:"api_v1_enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			v2Error(w, 400, "request:invalid")
			return
		}
		if err := d.State.update(body.CloudEnabled, body.APIv1Enabled); err != nil {
			v2Error(w, 500, "internal")
			return
		}
		d.v2System(w, r)
	}))
	mux.HandleFunc("PUT /api/identify", d.auth(func(w http.ResponseWriter, r *http.Request) {
		d.Log.Info("identify requested", "remote", r.RemoteAddr)
		w.WriteHeader(204)
	}))
	return logRequests(d.Log, mux)
}

func (d *Device) v2System(w http.ResponseWriter, r *http.Request) {
	cloud, v1 := d.State.settings()
	writeJSON(w, 200, map[string]any{
		"wifi_ssid": d.WifiSSID, "wifi_rssi_db": -50, "cloud_enabled": cloud,
		"uptime_s": int(time.Since(d.start).Seconds()), "status_led_brightness_pct": 100,
		"api_v1_enabled": v1,
	})
}

func (d *Device) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || d.State.userFor(strings.TrimSpace(tok)) == "" {
			v2Error(w, 401, "user:unauthorized")
			return
		}
		h(w, r)
	}
}

func logRequests(log *slog.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Debug("request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		h.ServeHTTP(w, r)
	})
}
