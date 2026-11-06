package hwapi

import (
	"math"
	"strconv"

	"github.com/hansbogert/dsmr2homewizard/internal/dsmr"
)

// measurement holds the HomeWizard view of a telegram, independent of API version. Fields
// the meter did not send stay nil and are omitted from the JSON, as the real device does.
type measurement struct {
	uniqueID, meterModel, timestamp string
	version, tariff                 int

	importTotal, exportTotal *float64
	importT, exportT         [4]*float64

	power         *float64
	phasePower    [3]*float64
	voltage       [3]*float64
	current       [3]*float64 // signed: negative when the phase exports
	frequency     *float64
	sag, swell    [3]*int
	anyFail       *int
	longFail      *int
	avgDemand     *float64
	monthPeak     *float64
	monthPeakTime string

	external []dsmr.MBus
}

func fromTelegram(t *dsmr.Telegram) measurement {
	m := measurement{
		uniqueID: t.EquipmentID, meterModel: t.Header, timestamp: t.Timestamp,
		version: t.Version, tariff: t.Tariff,
		importT: t.Import, exportT: t.Export,
		voltage: t.Voltage, frequency: t.Frequency,
		sag: t.SagCount, swell: t.SwellCount,
		anyFail: t.AnyPowerFail, longFail: t.LongPowerFail,
		avgDemand: watts(t.AverageDemand), monthPeak: watts(t.MonthPeak),
		monthPeakTime: t.MonthPeakTime, external: t.MBus,
	}
	m.importTotal = total(t.ImportTotal, t.Import)
	m.exportTotal = total(t.ExportTotal, t.Export)
	m.power = net(t.PowerDelivered, t.PowerReturned)
	for p := range 3 {
		m.phasePower[p] = net(t.PhaseDelivered[p], t.PhaseReturned[p])
		if c := t.Current[p]; c != nil {
			v := *c
			if m.phasePower[p] != nil && *m.phasePower[p] < 0 {
				v = -v
			}
			m.current[p] = &v
		}
	}
	return m
}

func total(sum *float64, parts [4]*float64) *float64 {
	if sum != nil {
		return sum
	}
	var s float64
	found := false
	for _, p := range parts {
		if p != nil {
			s += *p
			found = true
		}
	}
	if !found {
		return nil
	}
	s = math.Round(s*1000) / 1000
	return &s
}

// net returns delivered minus returned, converted from kW to W.
func net(delivered, returned *float64) *float64 {
	if delivered == nil && returned == nil {
		return nil
	}
	var v float64
	if delivered != nil {
		v += *delivered
	}
	if returned != nil {
		v -= *returned
	}
	return watts(&v)
}

func watts(kw *float64) *float64 {
	if kw == nil {
		return nil
	}
	w := math.Round(*kw * 1000)
	return &w
}

// v1Timestamp turns DSMR "YYMMDDhhmmssW" into the YYMMDDhhmmss number API v1 uses.
func v1Timestamp(s string) any {
	if len(s) < 12 {
		return nil
	}
	n, err := strconv.ParseInt(s[:12], 10, 64)
	if err != nil {
		return nil
	}
	return n
}

// v2Timestamp turns DSMR "YYMMDDhhmmssW" into "20YY-MM-DDThh:mm:ss".
func v2Timestamp(s string) any {
	if len(s) < 12 {
		return nil
	}
	if _, err := strconv.ParseInt(s[:12], 10, 64); err != nil {
		return nil
	}
	return "20" + s[0:2] + "-" + s[2:4] + "-" + s[4:6] + "T" + s[6:8] + ":" + s[8:10] + ":" + s[10:12]
}

var mbusTypes = map[int]string{
	3: "gas_meter", 4: "heat_meter", 6: "warm_water_meter", 7: "water_meter", 12: "inlet_heat_meter",
}

type fields map[string]any

func (f fields) put(key string, v any) {
	switch x := v.(type) {
	case nil:
		return
	case *float64:
		if x == nil {
			return
		}
		v = *x
	case *int:
		if x == nil {
			return
		}
		v = *x
	case string:
		if x == "" {
			return
		}
	}
	f[key] = v
}

func (f fields) putN(prefix, suffix string, vals [4]*float64) {
	for i, v := range vals {
		f.put(prefix+strconv.Itoa(i+1)+suffix, v)
	}
}

func (f fields) putPhases(prefix, suffix string, vals any) {
	for i := range 3 {
		var v any
		switch x := vals.(type) {
		case [3]*float64:
			v = x[i]
		case [3]*int:
			v = x[i]
		}
		f.put(prefix+strconv.Itoa(i+1)+suffix, v)
	}
}

func (m measurement) v1(wifiSSID string) fields {
	f := fields{"wifi_ssid": wifiSSID, "wifi_strength": 100}
	f.put("unique_id", m.uniqueID)
	f.put("smr_version", m.version)
	f.put("meter_model", m.meterModel)
	if m.tariff > 0 {
		f.put("active_tariff", m.tariff)
	}
	f.put("total_power_import_kwh", m.importTotal)
	f.putN("total_power_import_t", "_kwh", m.importT)
	f.put("total_power_export_kwh", m.exportTotal)
	f.putN("total_power_export_t", "_kwh", m.exportT)
	f.put("active_power_w", m.power)
	f.putPhases("active_power_l", "_w", m.phasePower)
	f.putPhases("active_voltage_l", "_v", m.voltage)
	f.putPhases("active_current_l", "_a", m.current)
	f.put("active_frequency_hz", m.frequency)
	f.putPhases("voltage_sag_l", "_count", m.sag)
	f.putPhases("voltage_swell_l", "_count", m.swell)
	f.put("any_power_fail_count", m.anyFail)
	f.put("long_power_fail_count", m.longFail)
	f.put("active_power_average_w", m.avgDemand)
	// "montly" is HomeWizard's spelling in API v1.
	f.put("montly_power_peak_w", m.monthPeak)
	f.put("montly_power_peak_timestamp", v1Timestamp(m.monthPeakTime))

	ext := []fields{}
	for _, d := range m.external {
		e := fields{}
		e.put("unique_id", d.ID)
		e.put("type", mbusTypes[d.Type])
		e.put("timestamp", v1Timestamp(d.Timestamp))
		e.put("value", d.Value)
		e.put("unit", d.Unit)
		ext = append(ext, e)
		if d.Type == 3 && f["total_gas_m3"] == nil {
			f.put("total_gas_m3", d.Value)
			f.put("gas_timestamp", v1Timestamp(d.Timestamp))
			f.put("gas_unique_id", d.ID)
		}
	}
	f["external"] = ext
	return f
}

func (m measurement) v2() fields {
	f := fields{}
	f.put("protocol_version", m.version)
	f.put("meter_model", m.meterModel)
	f.put("unique_id", m.uniqueID)
	f.put("timestamp", v2Timestamp(m.timestamp))
	if m.tariff > 0 {
		f.put("tariff", m.tariff)
	}
	f.put("energy_import_kwh", m.importTotal)
	f.putN("energy_import_t", "_kwh", m.importT)
	f.put("energy_export_kwh", m.exportTotal)
	f.putN("energy_export_t", "_kwh", m.exportT)
	f.put("power_w", m.power)
	f.putPhases("power_l", "_w", m.phasePower)
	f.putPhases("voltage_l", "_v", m.voltage)
	var sum float64
	found := false
	for _, c := range m.current {
		if c != nil {
			sum += math.Abs(*c)
			found = true
		}
	}
	if found {
		f.put("current_a", sum)
	}
	f.putPhases("current_l", "_a", m.current)
	f.put("frequency_hz", m.frequency)
	f.putPhases("voltage_sag_l", "_count", m.sag)
	f.putPhases("voltage_swell_l", "_count", m.swell)
	f.put("any_power_fail_count", m.anyFail)
	f.put("long_power_fail_count", m.longFail)
	f.put("average_power_15m_w", m.avgDemand)
	f.put("monthly_power_peak_w", m.monthPeak)
	f.put("monthly_power_peak_timestamp", v2Timestamp(m.monthPeakTime))

	ext := []fields{}
	for _, d := range m.external {
		e := fields{}
		e.put("unique_id", d.ID)
		e.put("type", mbusTypes[d.Type])
		e.put("timestamp", v2Timestamp(d.Timestamp))
		e.put("value", d.Value)
		e.put("unit", d.Unit)
		ext = append(ext, e)
	}
	if len(ext) > 0 {
		f["external"] = ext
	}
	return f
}
