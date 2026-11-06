// Package dsmr parses DSMR (Dutch/Belgian smart meter P1 port) telegrams.
package dsmr

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MBus is a device on the meter's M-Bus (gas, water, heat).
type MBus struct {
	Channel   int
	Type      int    // M-Bus device type: 3 gas, 4 heat, 6 warm water, 7 water, 12 inlet heat
	ID        string // equipment identifier as sent (hex-encoded ASCII)
	Timestamp string // YYMMDDhhmmss[WS]
	Value     *float64
	Unit      string
}

// Telegram holds the values HomeWizard exposes. Pointers are nil when the meter did not send
// the field. Power values are in kW as sent by the meter; energy in kWh.
type Telegram struct {
	Raw         string
	Header      string // identification line without the leading '/'
	Version     int    // 1-3:0.2.8, e.g. 50 for DSMR 5.0
	Timestamp   string // 0-0:1.0.0, YYMMDDhhmmss[WS]
	EquipmentID string // 0-0:96.1.1
	Tariff      int    // 0-0:96.14.0

	ImportTotal *float64    // 1-0:1.8.0
	Import      [4]*float64 // 1-0:1.8.1 .. 1.8.4
	ExportTotal *float64    // 1-0:2.8.0
	Export      [4]*float64 // 1-0:2.8.1 .. 2.8.4

	PowerDelivered *float64    // 1-0:1.7.0
	PowerReturned  *float64    // 1-0:2.7.0
	PhaseDelivered [3]*float64 // 1-0:21/41/61.7.0
	PhaseReturned  [3]*float64 // 1-0:22/42/62.7.0
	Voltage        [3]*float64 // 1-0:32/52/72.7.0
	Current        [3]*float64 // 1-0:31/51/71.7.0, unsigned in DSMR
	Frequency      *float64    // 1-0:14.7.0 (rare)
	SagCount       [3]*int     // 1-0:32/52/72.32.0
	SwellCount     [3]*int     // 1-0:32/52/72.36.0
	AnyPowerFail   *int        // 0-0:96.7.21
	LongPowerFail  *int        // 0-0:96.7.9
	AverageDemand  *float64    // 1-0:1.4.0 (Belgian capacity tariff)
	MonthPeak      *float64    // 1-0:1.6.0 value
	MonthPeakTime  string      // 1-0:1.6.0 timestamp
	MBus           []MBus
}

var lineRe = regexp.MustCompile(`^(\d+-\d+:\d+\.\d+\.\d+)((?:\([^)]*\))+)\s*$`)

// ErrCRC is returned when the telegram's checksum does not match.
var ErrCRC = errors.New("dsmr: CRC mismatch")

// Parse parses one complete telegram ('/' through the '!' line). The CRC is verified when the
// telegram carries one (DSMR 4+). If it fails and the telegram uses bare LF line endings — as
// happens after a trip through MQTT or a text pipeline — it is retried with CRLF.
func Parse(raw string) (*Telegram, error) {
	raw = strings.TrimLeft(raw, "\r\n\x00 ")
	if !strings.HasPrefix(raw, "/") {
		return nil, errors.New("dsmr: telegram does not start with '/'")
	}
	bang := strings.LastIndexByte(raw, '!')
	if bang < 0 {
		return nil, errors.New("dsmr: telegram has no '!' end marker")
	}
	crcText := strings.TrimSpace(raw[bang+1:])
	if crcText != "" {
		if err := checkCRC(raw[:bang+1], crcText); err != nil {
			if strings.Contains(raw[:bang], "\r\n") {
				return nil, err
			}
			fixed := strings.ReplaceAll(raw[:bang+1], "\n", "\r\n")
			if checkCRC(fixed, crcText) != nil {
				return nil, err
			}
			raw = fixed + crcText + "\r\n"
			bang = len(fixed) - 1
		}
	}

	t := &Telegram{Raw: raw}
	lines := strings.Split(strings.ReplaceAll(raw[:bang], "\r", ""), "\n")
	t.Header = strings.TrimPrefix(strings.TrimSpace(lines[0]), "/")
	mbus := map[int]*MBus{}
	for _, line := range lines[1:] {
		m := lineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		t.apply(m[1], splitGroups(m[2]), mbus)
	}
	for ch := 1; ch <= 4; ch++ {
		if d, ok := mbus[ch]; ok {
			t.MBus = append(t.MBus, *d)
		}
	}
	return t, nil
}

func checkCRC(body, crcText string) error {
	want, err := strconv.ParseUint(crcText, 16, 16)
	if err != nil {
		return fmt.Errorf("dsmr: bad CRC field %q", crcText)
	}
	if got := CRC16([]byte(body)); got != uint16(want) {
		return fmt.Errorf("%w: got %04X, telegram says %04X", ErrCRC, got, want)
	}
	return nil
}

func splitGroups(s string) []string {
	s = strings.TrimSuffix(strings.TrimPrefix(s, "("), ")")
	return strings.Split(s, ")(")
}

// value splits "001234.567*kWh" into 1234.567 and "kWh".
func value(s string) (*float64, string) {
	num, unit, _ := strings.Cut(s, "*")
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return nil, unit
	}
	return &f, unit
}

func intValue(s string) *int {
	num, _, _ := strings.Cut(s, "*")
	i, err := strconv.Atoi(num)
	if err != nil {
		return nil
	}
	return &i
}

var phaseIndex = map[string]int{"21": 0, "41": 1, "61": 2, "22": 0, "42": 1, "62": 2,
	"32": 0, "52": 1, "72": 2, "31": 0, "51": 1, "71": 2}

func (t *Telegram) apply(obis string, g []string, mbus map[int]*MBus) {
	first := g[0]
	f, _ := value(first)
	switch obis {
	case "1-3:0.2.8":
		if v := intValue(first); v != nil {
			t.Version = *v
		}
		return
	case "0-0:96.1.4": // Belgian e-MUCS version, e.g. 50217 for 5.0.2.17
		if v := intValue(first); v != nil && t.Version == 0 {
			t.Version = *v / 1000
		}
		return
	case "0-0:1.0.0":
		t.Timestamp = first
		return
	case "0-0:96.1.1", "0-0:96.1.0":
		t.EquipmentID = first
		return
	case "0-0:96.14.0":
		if v := intValue(first); v != nil {
			t.Tariff = *v
		}
		return
	case "1-0:1.8.0":
		t.ImportTotal = f
		return
	case "1-0:2.8.0":
		t.ExportTotal = f
		return
	case "1-0:1.7.0":
		t.PowerDelivered = f
		return
	case "1-0:2.7.0":
		t.PowerReturned = f
		return
	case "1-0:14.7.0":
		t.Frequency = f
		return
	case "0-0:96.7.21":
		t.AnyPowerFail = intValue(first)
		return
	case "0-0:96.7.9":
		t.LongPowerFail = intValue(first)
		return
	case "1-0:1.4.0":
		t.AverageDemand = f
		return
	case "1-0:1.6.0":
		if len(g) >= 2 {
			t.MonthPeakTime = g[0]
			t.MonthPeak, _ = value(g[1])
		}
		return
	}

	// Tariff registers 1-0:1.8.x / 2.8.x.
	if len(obis) == 9 && obis[:4] == "1-0:" && obis[6:8] == "8." {
		idx := int(obis[8] - '1')
		if idx >= 0 && idx < 4 {
			switch obis[4:6] {
			case "1.":
				t.Import[idx] = f
			case "2.":
				t.Export[idx] = f
			}
		}
		return
	}

	// Per-phase instantaneous values 1-0:XX.7.0 and quality counters 1-0:XX.32.0 / XX.36.0.
	if strings.HasPrefix(obis, "1-0:") {
		code, rest, _ := strings.Cut(obis[4:], ".")
		if p, ok := phaseIndex[code]; ok {
			switch {
			case rest == "7.0" && (code == "21" || code == "41" || code == "61"):
				t.PhaseDelivered[p] = f
			case rest == "7.0" && (code == "22" || code == "42" || code == "62"):
				t.PhaseReturned[p] = f
			case rest == "7.0" && (code == "32" || code == "52" || code == "72"):
				t.Voltage[p] = f
			case rest == "7.0" && (code == "31" || code == "51" || code == "71"):
				t.Current[p] = f
			case rest == "32.0":
				t.SagCount[p] = intValue(first)
			case rest == "36.0":
				t.SwellCount[p] = intValue(first)
			}
		}
		return
	}

	// M-Bus devices 0-n:24.1.0 (type), 0-n:96.1.0 (id), 0-n:24.2.1 / 24.2.3 (ts)(value).
	if len(obis) > 4 && obis[0] == '0' && obis[1] == '-' {
		ch, err := strconv.Atoi(obis[2:strings.IndexByte(obis, ':')])
		if err != nil || ch < 1 || ch > 4 {
			return
		}
		d := mbus[ch]
		if d == nil {
			d = &MBus{Channel: ch}
			mbus[ch] = d
		}
		switch obis[strings.IndexByte(obis, ':')+1:] {
		case "24.1.0":
			if v := intValue(first); v != nil {
				d.Type = *v
			}
		case "96.1.0":
			d.ID = first
		case "24.2.1", "24.2.3":
			if len(g) >= 2 {
				d.Timestamp = g[0]
				d.Value, d.Unit = value(g[1])
			}
		}
	}
}

// CRC16 is CRC-16/ARC (poly 0xA001 reflected, init 0) as specified by DSMR 4+.
func CRC16(b []byte) uint16 {
	var crc uint16
	for _, c := range b {
		crc ^= uint16(c)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}
