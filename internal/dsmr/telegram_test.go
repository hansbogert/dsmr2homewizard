package dsmr

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Adapted from the DSMR 5.0.2 P1 companion standard example telegram.
const sampleBody = "/ISk5\\2MT382-1000\r\n" +
	"\r\n" +
	"1-3:0.2.8(50)\r\n" +
	"0-0:1.0.0(101209113020W)\r\n" +
	"0-0:96.1.1(4B384547303034303436333935353037)\r\n" +
	"1-0:1.8.1(123456.789*kWh)\r\n" +
	"1-0:1.8.2(123456.789*kWh)\r\n" +
	"1-0:2.8.1(123456.789*kWh)\r\n" +
	"1-0:2.8.2(123456.789*kWh)\r\n" +
	"0-0:96.14.0(0002)\r\n" +
	"1-0:1.7.0(01.193*kW)\r\n" +
	"1-0:2.7.0(00.000*kW)\r\n" +
	"0-0:96.7.21(00004)\r\n" +
	"0-0:96.7.9(00002)\r\n" +
	"1-0:99.97.0(2)(0-0:96.7.19)(101208152415W)(0000000240*s)(101208151004W)(0000000301*s)\r\n" +
	"1-0:32.32.0(00002)\r\n" +
	"1-0:52.32.0(00001)\r\n" +
	"1-0:72.32.0(00000)\r\n" +
	"1-0:32.36.0(00000)\r\n" +
	"1-0:52.36.0(00003)\r\n" +
	"1-0:72.36.0(00000)\r\n" +
	"0-0:96.13.0()\r\n" +
	"1-0:32.7.0(220.1*V)\r\n" +
	"1-0:52.7.0(220.2*V)\r\n" +
	"1-0:72.7.0(220.3*V)\r\n" +
	"1-0:31.7.0(001*A)\r\n" +
	"1-0:51.7.0(002*A)\r\n" +
	"1-0:71.7.0(003*A)\r\n" +
	"1-0:21.7.0(01.111*kW)\r\n" +
	"1-0:41.7.0(02.222*kW)\r\n" +
	"1-0:61.7.0(03.333*kW)\r\n" +
	"1-0:22.7.0(04.444*kW)\r\n" +
	"1-0:42.7.0(05.555*kW)\r\n" +
	"1-0:62.7.0(06.666*kW)\r\n" +
	"0-1:24.1.0(003)\r\n" +
	"0-1:96.1.0(3232323241424344313233343536373839)\r\n" +
	"0-1:24.2.1(101209112500W)(12785.123*m3)\r\n" +
	"!"

func sample() string {
	return fmt.Sprintf("%s%04X\r\n", sampleBody, CRC16([]byte(sampleBody)))
}

func TestCRC16CheckValue(t *testing.T) {
	// Standard CRC-16/ARC check value.
	if got := CRC16([]byte("123456789")); got != 0xBB3D {
		t.Fatalf("CRC16 = %04X, want BB3D", got)
	}
}

func TestParse(t *testing.T) {
	tg, err := Parse(sample())
	if err != nil {
		t.Fatal(err)
	}
	if tg.Version != 50 || tg.Tariff != 2 || tg.Header != `ISk5\2MT382-1000` {
		t.Errorf("version/tariff/header = %d/%d/%q", tg.Version, tg.Tariff, tg.Header)
	}
	if *tg.Import[0] != 123456.789 || *tg.Export[1] != 123456.789 {
		t.Errorf("tariff registers wrong")
	}
	if *tg.PowerDelivered != 1.193 || *tg.PowerReturned != 0 {
		t.Errorf("power wrong")
	}
	if *tg.PhaseDelivered[2] != 3.333 || *tg.PhaseReturned[0] != 4.444 {
		t.Errorf("phase power wrong")
	}
	if *tg.Voltage[1] != 220.2 || *tg.Current[2] != 3 {
		t.Errorf("voltage/current wrong")
	}
	if *tg.SagCount[0] != 2 || *tg.SwellCount[1] != 3 || *tg.AnyPowerFail != 4 || *tg.LongPowerFail != 2 {
		t.Errorf("counters wrong")
	}
	if len(tg.MBus) != 1 || tg.MBus[0].Type != 3 || *tg.MBus[0].Value != 12785.123 ||
		tg.MBus[0].Unit != "m3" || tg.MBus[0].Timestamp != "101209112500W" {
		t.Errorf("mbus wrong: %+v", tg.MBus)
	}
}

func TestParseBadCRC(t *testing.T) {
	bad := sampleBody + "0000\r\n"
	if _, err := Parse(bad); !errors.Is(err, ErrCRC) {
		t.Fatalf("err = %v, want ErrCRC", err)
	}
}

func TestParseLFOnly(t *testing.T) {
	lf := strings.ReplaceAll(sample(), "\r\n", "\n")
	tg, err := Parse(lf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tg.Raw, "\r\n") {
		t.Errorf("raw telegram not restored to CRLF")
	}
}

func TestReaderSkipsPartial(t *testing.T) {
	stream := "1-0:1.7.0(00.100*kW)\r\n!1234\r\n" + sample() + sample()
	r := NewReader(strings.NewReader(stream))
	for i := 0; i < 2; i++ {
		raw, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(raw); err != nil {
			t.Fatalf("telegram %d: %v", i, err)
		}
	}
}
