package hwapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"

	"github.com/hansbogert/dsmr2homewizard/internal/dsmr"
)

// v1Snapshot is a measurement in HomeWizard API v1 field names, for inputs that only have
// parsed values (e.g. openHAB's DSMR binding publishing to MQTT). Power and current are signed:
// negative when exporting.
type v1Snapshot struct {
	UniqueID     string `json:"unique_id"`
	SMRVersion   int    `json:"smr_version"`
	MeterModel   string `json:"meter_model"`
	ActiveTariff int    `json:"active_tariff"`

	ImportKWh   *float64 `json:"total_power_import_kwh"`
	ImportT1KWh *float64 `json:"total_power_import_t1_kwh"`
	ImportT2KWh *float64 `json:"total_power_import_t2_kwh"`
	ImportT3KWh *float64 `json:"total_power_import_t3_kwh"`
	ImportT4KWh *float64 `json:"total_power_import_t4_kwh"`
	ExportKWh   *float64 `json:"total_power_export_kwh"`
	ExportT1KWh *float64 `json:"total_power_export_t1_kwh"`
	ExportT2KWh *float64 `json:"total_power_export_t2_kwh"`
	ExportT3KWh *float64 `json:"total_power_export_t3_kwh"`
	ExportT4KWh *float64 `json:"total_power_export_t4_kwh"`

	PowerW   *float64 `json:"active_power_w"`
	PowerL1W *float64 `json:"active_power_l1_w"`
	PowerL2W *float64 `json:"active_power_l2_w"`
	PowerL3W *float64 `json:"active_power_l3_w"`
	VoltL1   *float64 `json:"active_voltage_l1_v"`
	VoltL2   *float64 `json:"active_voltage_l2_v"`
	VoltL3   *float64 `json:"active_voltage_l3_v"`
	CurL1    *float64 `json:"active_current_l1_a"`
	CurL2    *float64 `json:"active_current_l2_a"`
	CurL3    *float64 `json:"active_current_l3_a"`
	FreqHz   *float64 `json:"active_frequency_hz"`

	SagL1    *int `json:"voltage_sag_l1_count"`
	SagL2    *int `json:"voltage_sag_l2_count"`
	SagL3    *int `json:"voltage_sag_l3_count"`
	SwellL1  *int `json:"voltage_swell_l1_count"`
	SwellL2  *int `json:"voltage_swell_l2_count"`
	SwellL3  *int `json:"voltage_swell_l3_count"`
	AnyFail  *int `json:"any_power_fail_count"`
	LongFail *int `json:"long_power_fail_count"`

	AvgPowerW     *float64 `json:"active_power_average_w"`
	MonthPeakW    *float64 `json:"montly_power_peak_w"`
	MonthPeakTime *int64   `json:"montly_power_peak_timestamp"`

	GasM3       *float64 `json:"total_gas_m3"`
	GasTime     *int64   `json:"gas_timestamp"`
	GasUniqueID string   `json:"gas_unique_id"`
}

// ParseV1JSON turns a v1 snapshot into the telegram model the API serves from. The result has
// no Raw telegram. Unknown fields are rejected so a typo in the publisher shows up in the log
// instead of as a silently missing value.
func ParseV1JSON(b []byte) (*dsmr.Telegram, error) {
	var s v1Snapshot
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("json snapshot: %w", err)
	}
	if s.PowerW == nil {
		return nil, fmt.Errorf("json snapshot: active_power_w is required")
	}
	t := &dsmr.Telegram{
		EquipmentID: s.UniqueID, Version: s.SMRVersion, Header: s.MeterModel, Tariff: s.ActiveTariff,
		ImportTotal: s.ImportKWh, ExportTotal: s.ExportKWh,
		Import:       [4]*float64{s.ImportT1KWh, s.ImportT2KWh, s.ImportT3KWh, s.ImportT4KWh},
		Export:       [4]*float64{s.ExportT1KWh, s.ExportT2KWh, s.ExportT3KWh, s.ExportT4KWh},
		Voltage:      [3]*float64{s.VoltL1, s.VoltL2, s.VoltL3},
		Frequency:    s.FreqHz,
		SagCount:     [3]*int{s.SagL1, s.SagL2, s.SagL3},
		SwellCount:   [3]*int{s.SwellL1, s.SwellL2, s.SwellL3},
		AnyPowerFail: s.AnyFail, LongPowerFail: s.LongFail,
		AverageDemand: kw(s.AvgPowerW), MonthPeak: kw(s.MonthPeakW),
		MonthPeakTime: dsmrTime(s.MonthPeakTime),
	}
	t.PowerDelivered, t.PowerReturned = split(s.PowerW)
	for p, w := range [3]*float64{s.PowerL1W, s.PowerL2W, s.PowerL3W} {
		t.PhaseDelivered[p], t.PhaseReturned[p] = split(w)
	}
	// The model holds DSMR's unsigned currents; the API re-derives the sign from phase power.
	for p, c := range [3]*float64{s.CurL1, s.CurL2, s.CurL3} {
		if c != nil {
			a := math.Abs(*c)
			t.Current[p] = &a
		}
	}
	if s.GasM3 != nil {
		t.MBus = []dsmr.MBus{{Channel: 1, Type: 3, ID: s.GasUniqueID, Value: s.GasM3,
			Timestamp: dsmrTime(s.GasTime)}}
	}
	return t, nil
}

// split turns signed watts into DSMR's delivered/returned kW pair.
func split(w *float64) (delivered, returned *float64) {
	if w == nil {
		return nil, nil
	}
	d, r := 0.0, 0.0
	if *w >= 0 {
		d = *w / 1000
	} else {
		r = -*w / 1000
	}
	return &d, &r
}

func kw(w *float64) *float64 {
	if w == nil {
		return nil
	}
	v := *w / 1000
	return &v
}

// dsmrTime turns a v1 YYMMDDhhmmss number back into DSMR's string form.
func dsmrTime(n *int64) string {
	if n == nil {
		return ""
	}
	return fmt.Sprintf("%012d", *n)
}
