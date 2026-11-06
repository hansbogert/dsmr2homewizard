package hwapi

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

const snapshot = `{"unique_id":"4530","smr_version":50,"meter_model":"ISK5","active_tariff":2,
"total_power_import_t1_kwh":10830.511,"total_power_import_t2_kwh":2948.827,
"total_power_export_t1_kwh":1,"total_power_export_t2_kwh":2,
"active_power_w":-543,"active_power_l1_w":-676,"active_power_l2_w":133,
"active_voltage_l1_v":230.1,"active_current_l1_a":4,"active_current_l2_a":2,
"total_gas_m3":2569.646,"gas_timestamp":240628140000}`

func TestParseV1JSON(t *testing.T) {
	tg, err := ParseV1JSON([]byte(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := LoadState(t.TempDir(), "3c39e7aabbcc", "")
	l := &Latest{MaxAge: time.Minute}
	l.Set(tg)
	d := NewDevice(st, l, "ssid", slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, m := do(t, d.V1Handler(), "GET", "/api/v1/data", "", "")
	want := map[string]any{
		"active_power_w": -543.0, "active_power_l1_w": -676.0, "active_power_l2_w": 133.0,
		"active_current_l1_a": -4.0, "active_current_l2_a": 2.0, "active_current_a": 6.0,
		"total_power_import_kwh": 13779.338, "total_power_export_kwh": 3.0,
		"active_tariff": 2.0, "total_gas_m3": 2569.646, "gas_timestamp": 240628140000.0,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	tg2, _ := ParseV1JSON([]byte(`{"active_power_w":1}`))
	l.Set(tg2)
	if _, m := do(t, d.V1Handler(), "GET", "/api/v1/data", "", ""); m["smr_version"] != nil {
		t.Errorf("smr_version = %v for a snapshot without it, want omitted", m["smr_version"])
	}
	if code, _ := do(t, d.V1Handler(), "GET", "/api/v1/telegram", "", ""); code != 404 {
		t.Errorf("telegram without raw input: %d, want 404", code)
	}
}

func TestParseV1JSONRejects(t *testing.T) {
	for name, in := range map[string]string{
		"typo":          `{"active_power_w":1,"active_powr_l1_w":1}`,
		"no power":      `{"total_power_import_t1_kwh":1}`,
		"not an object": `[1]`,
	} {
		if _, err := ParseV1JSON([]byte(in)); err == nil {
			t.Errorf("%s: accepted %s", name, strings.TrimSpace(in))
		}
	}
}
