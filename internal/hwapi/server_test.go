package hwapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hansbogert/dsmr2homewizard/internal/dsmr"
)

// Three-phase meter, exporting on L1, gas on M-Bus channel 1.
const body = "/ISK5\\2M550T-1012\r\n\r\n" +
	"1-3:0.2.8(50)\r\n" +
	"0-0:1.0.0(240628141234S)\r\n" +
	"0-0:96.1.1(4530303434303037333731363237363136)\r\n" +
	"1-0:1.8.1(010830.511*kWh)\r\n" +
	"1-0:1.8.2(002948.827*kWh)\r\n" +
	"1-0:2.8.1(000000.000*kWh)\r\n" +
	"1-0:2.8.2(000000.000*kWh)\r\n" +
	"0-0:96.14.0(0002)\r\n" +
	"1-0:1.7.0(00.133*kW)\r\n" +
	"1-0:2.7.0(00.676*kW)\r\n" +
	"1-0:32.7.0(230.1*V)\r\n" +
	"1-0:31.7.0(004*A)\r\n" +
	"1-0:51.7.0(002*A)\r\n" +
	"1-0:21.7.0(00.000*kW)\r\n" +
	"1-0:41.7.0(00.133*kW)\r\n" +
	"1-0:22.7.0(00.676*kW)\r\n" +
	"1-0:42.7.0(00.000*kW)\r\n" +
	"0-1:24.1.0(003)\r\n" +
	"0-1:96.1.0(4730303339303031363532303530323136)\r\n" +
	"0-1:24.2.1(240628140000S)(02569.646*m3)\r\n" +
	"!"

func newDevice(t *testing.T) (*Device, *Latest) {
	t.Helper()
	st, err := LoadState(t.TempDir(), "3c39e7aabbcc", "")
	if err != nil {
		t.Fatal(err)
	}
	l := &Latest{MaxAge: time.Minute}
	raw := fmt.Sprintf("%s%04X\r\n", body, dsmr.CRC16([]byte(body)))
	tg, err := dsmr.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	l.Set(tg)
	return NewDevice(st, l, "ssid", slog.New(slog.NewTextHandler(io.Discard, nil))), l
}

func do(t *testing.T, h http.Handler, method, path, token, reqBody string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(reqBody))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestV1Data(t *testing.T) {
	d, _ := newDevice(t)
	code, m := do(t, d.V1Handler(), "GET", "/api/v1/data", "", "")
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	want := map[string]any{
		"smr_version": 50.0, "active_tariff": 2.0,
		"total_power_import_kwh":    13779.338,
		"total_power_import_t1_kwh": 10830.511,
		"active_power_w":            -543.0,
		"active_power_l1_w":         -676.0,
		"active_power_l2_w":         133.0,
		"active_current_l1_a":       -4.0,
		"active_current_l2_a":       2.0,
		"active_voltage_l1_v":       230.1,
		"total_gas_m3":              2569.646,
		"gas_timestamp":             240628140000.0,
		"meter_model":               `ISK5\2M550T-1012`,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	if _, ok := m["active_power_l3_w"]; ok {
		t.Errorf("L3 present although the meter sent none")
	}
}

func TestV1StaleAndDisabled(t *testing.T) {
	d, l := newDevice(t)
	l.MaxAge = time.Nanosecond
	time.Sleep(time.Millisecond)
	if code, _ := do(t, d.V1Handler(), "GET", "/api/v1/data", "", ""); code != 503 {
		t.Errorf("stale: code %d, want 503", code)
	}
	f := false
	_ = d.State.update(nil, &f)
	if code, _ := do(t, d.V1Handler(), "GET", "/api/v1/telegram", "", ""); code != 403 {
		t.Errorf("disabled: code %d, want 403", code)
	}
	if code, _ := do(t, d.V1Handler(), "GET", "/api", "", ""); code != 200 {
		t.Errorf("/api must stay reachable when v1 is disabled, got %d", code)
	}
}

func TestV2PairingAndMeasurement(t *testing.T) {
	d, _ := newDevice(t)
	h := d.V2Handler()
	if code, _ := do(t, h, "GET", "/api/measurement", "", ""); code != 401 {
		t.Fatalf("unauthenticated: code %d", code)
	}
	if code, m := do(t, h, "POST", "/api/user", "", `{"name":"local/test"}`); code != 403 ||
		m["error"] != "user:creation-not-enabled" {
		t.Fatalf("closed pairing: %d %v", code, m)
	}
	d.OpenPairing(time.Minute)
	code, m := do(t, h, "POST", "/api/user", "", `{"name":"local/test"}`)
	tok, _ := m["token"].(string)
	if code != 200 || len(tok) != 32 {
		t.Fatalf("pairing: %d %v", code, m)
	}
	code, m = do(t, h, "GET", "/api/measurement", tok, "")
	if code != 200 || m["power_w"] != -543.0 || m["timestamp"] != "2024-06-28T14:12:34" ||
		m["energy_import_kwh"] != 13779.338 || m["current_a"] != 6.0 {
		t.Fatalf("measurement: %d %v", code, m)
	}
	ext, _ := m["external"].([]any)
	if len(ext) != 1 || ext[0].(map[string]any)["type"] != "gas_meter" {
		t.Errorf("external = %v", m["external"])
	}
	if code, _ := do(t, h, "DELETE", "/api/user", tok, `{"name":"local/test"}`); code != 204 {
		t.Errorf("delete: %d", code)
	}
	if code, _ := do(t, h, "GET", "/api/measurement", tok, ""); code != 401 {
		t.Errorf("deleted token still works: %d", code)
	}
}

func TestClientQuirks(t *testing.T) {
	d, _ := newDevice(t)
	v1 := d.V1Handler()
	if code, m := do(t, v1, "GET", "/api/", "", ""); code != 200 || m["product_type"] != "HWE-P1" {
		t.Errorf("openHAB /api/: %d %v", code, m)
	}
	if code, _ := do(t, v1, "GET", "/api/v1/state", "", ""); code != 404 {
		t.Errorf("HA probes /api/v1/state and needs 404, got %d", code)
	}
	if _, m := do(t, v1, "GET", "/api/v1/data", "", ""); m["active_current_a"] != 6.0 {
		t.Errorf("active_current_a = %v, want 6", m["active_current_a"])
	}

	v2 := d.V2Handler()
	d.OpenPairing(time.Minute)
	_, m := do(t, v2, "POST", "/api/user", "", `{"name":"local/home-assistant#ab12cd"}`)
	tok, _ := m["token"].(string)
	if tok == "" {
		t.Fatalf("pairing with '#' in name failed: %v", m)
	}
	if code, m := do(t, v2, "GET", "/api/batteries", tok, ""); code != 200 || m["mode"] != "zero" {
		t.Errorf("batteries: %d %v", code, m)
	}
	if code, _ := do(t, v2, "PUT", "/api/system/identify", tok, ""); code != 204 {
		t.Errorf("identify: %d", code)
	}
	if code, m := do(t, v2, "GET", "/api", "", ""); code != 200 || m["api_version"] != "2.0.0" {
		t.Errorf("v2 /api must answer 200 unauthenticated (keeps HA on v1): %d %v", code, m)
	}
}
