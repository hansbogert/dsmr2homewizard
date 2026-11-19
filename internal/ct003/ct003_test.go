package ct003

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hansbogert/dsmr2homewizard/internal/dsmr"
)

const ctMAC = "3c39e7aabbcc"

func TestFrameLength(t *testing.T) {
	// The request from the protocol notes: its length field reads 53.
	b := frame("|HMG-50|AABBCCDDEEFF|HME-4|112233445566|B|-217")
	if len(b) != 53 || !strings.HasPrefix(string(b[2:]), "53|") {
		t.Fatalf("len %d, packet %q", len(b), b)
	}
	// Lengths around the two-to-three digit boundary count their own digits.
	for n := 80; n < 110; n++ {
		b := frame("|" + strings.Repeat("x", n))
		f := strings.SplitN(string(b[2:]), "|", 2)[0]
		if f != fmt.Sprint(len(b)) {
			t.Errorf("body %d: length field %s, packet is %d bytes", n, f, len(b))
		}
		if _, err := unframe(b); err != nil {
			t.Errorf("body %d: %v", n, err)
		}
	}
}

func TestUnframeChecksum(t *testing.T) {
	b := frame("|HMG-50|AABBCCDDEEFF|HME-3|000000000000|A|12")
	if _, err := unframe(b); err != nil {
		t.Fatal(err)
	}
	bad := append([]byte{}, b...)
	bad[10] ^= 1
	if _, err := unframe(bad); err == nil {
		t.Error("corrupted packet accepted")
	}
	// Checksums are hex in either case.
	lower := append([]byte{}, b[:len(b)-2]...)
	lower = append(lower, []byte(strings.ToLower(string(b[len(b)-2:])))...)
	if _, err := unframe(lower); err != nil {
		t.Errorf("lowercase checksum: %v", err)
	}
}

func req(fields ...string) []byte { return frame("|" + strings.Join(fields, "|")) }

func TestParseRequest(t *testing.T) {
	r, err := parseRequest(req("HMG-50", "aabbccddeeff", "HME-3", Wildcard, "B", "-217"))
	if err != nil {
		t.Fatal(err)
	}
	if r.devType != "HMG-50" || r.devMAC != "aabbccddeeff" || r.ctType != "HME-3" ||
		r.ctMAC != Wildcard || r.phase != 'B' || r.power != -217 || !r.participate {
		t.Errorf("got %+v", r)
	}

	r, _ = parseRequest(req("HMJ-2", "aabbccddeeff", "HME-3", Wildcard, "D", "100", "0"))
	if r.participate || r.phase != 'D' {
		t.Errorf("participate=0, phase D: got %+v", r)
	}
	for _, p := range []string{"0", "", "E"} {
		r, err := parseRequest(req("HMG-50", "m", "HME-3", Wildcard, p, "0"))
		if err != nil || r.phase != '0' {
			t.Errorf("phase %q: got %q, %v", p, r.phase, err)
		}
	}
	r, _ = parseRequest(req("HMJ-2", "m", "HME-3", Wildcard, "A", "99999"))
	if r.power != 32767 {
		t.Errorf("power clamps to int16: got %d", r.power)
	}

	for name, b := range map[string][]byte{
		"five fields":     req("HMG-50", "m", "HME-3", Wildcard, "A"),
		"long type":       req("HMG-50-LONGER", "m", "HME-3", Wildcard, "A", "0"),
		"empty mac":       req("HMG-50", "", "HME-3", Wildcard, "A", "0"),
		"long ct mac":     req("HMG-50", "m", "HME-3", Wildcard+"00", "A", "0"),
		"two-char phase":  req("HMG-50", "m", "HME-3", Wildcard, "AB", "0"),
		"non-int power":   req("HMG-50", "m", "HME-3", Wildcard, "A", "1.5"),
		"no frame at all": []byte("53|HMG-50|m|HME-3|000000000000|A|0"),
	} {
		if _, err := parseRequest(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func f(v float64) *float64 { return &v }

// telegram: L1 exporting 676 W, L2 importing 133 W, L3 importing 20 W; net 543 W export.
func telegram() *dsmr.Telegram {
	return &dsmr.Telegram{
		Import:         [4]*float64{f(10830.511), f(2948.827)},
		Export:         [4]*float64{f(1.5), f(0)},
		PowerDelivered: f(0), PowerReturned: f(0.543),
		PhaseDelivered: [3]*float64{f(0), f(0.133), f(0.020)},
		PhaseReturned:  [3]*float64{f(0.676), f(0), f(0)},
	}
}

func newServer(t *testing.T, get func() *dsmr.Telegram) (*Server, net.Addr) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := New(ctMAC, get, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Pause = 0
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, conn) }()
	t.Cleanup(func() { cancel(); <-done })
	return s, conn.LocalAddr()
}

type battery struct {
	t    *testing.T
	conn net.PacketConn
	ct   net.Addr
}

func newBattery(t *testing.T, ct net.Addr) battery {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return battery{t, conn, ct}
}

// poll sends a request and returns the reply's fields, or nil when none comes.
func (b battery) poll(fields ...string) []string {
	b.t.Helper()
	if _, err := b.conn.WriteTo(req(fields...), b.ct); err != nil {
		b.t.Fatal(err)
	}
	b.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	buf := make([]byte, 1500)
	n, _, err := b.conn.ReadFrom(buf)
	if err != nil {
		return nil
	}
	fs, err := unframe(buf[:n])
	if err != nil {
		b.t.Fatalf("reply %q: %v", buf[:n], err)
	}
	return fs
}

func TestReply(t *testing.T) {
	_, addr := newServer(t, telegram)
	bat := newBattery(t, addr)

	got := bat.poll("HMG-50", "aabbccddeeff", "HME-3", Wildcard, "A", "-300")
	want := []string{"HME-3", ctMAC, "HMG-50", "aabbccddeeff",
		"-676", "133", "20", "-543", // per-phase and total power
		"1", "0", "0", "0", // batteries on A, B, C, combined
		"-50", "1", // RSSI, info_idx
		"0", "-300", "0", "0", "0", // charging: unassigned, A, B, C, combined
		"0", "0", "0", "0", "0", // discharging
		"108305110", "29488270", "15000", "0", // energy, 0.1 Wh
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("reply\n got %v\nwant %v", got, want)
	}

	// A second battery on B discharging; it is counted and info_idx moves on.
	bat2 := newBattery(t, addr)
	got = bat2.poll("HMG-50", "112233445566", "HME-3", ctMAC, "B", "250")
	if got == nil || got[3] != "112233445566" || got[8] != "1" || got[9] != "1" ||
		got[13] != "2" || got[15] != "-300" || got[21] != "250" {
		t.Errorf("second battery: %v", got)
	}
}

func TestNotAnswered(t *testing.T) {
	_, addr := newServer(t, telegram)
	bat := newBattery(t, addr)
	for name, fields := range map[string][]string{
		"other CT MAC":  {"HMG-50", "m", "HME-3", "001122334455", "A", "0"},
		"CT MAC case":   {"HMG-50", "m", "HME-3", strings.ToUpper(ctMAC), "A", "0"},
		"CT002 request": {"HMG-50", "m", "HME-4", Wildcard, "A", "0"},
	} {
		if got := bat.poll(fields...); got != nil {
			t.Errorf("%s: answered %v", name, got)
		}
	}
	if _, err := bat.conn.WriteTo([]byte("Hello"), addr); err != nil {
		t.Fatal(err)
	}
	if got := bat.poll("HMG-50", "m", "HME-3", Wildcard, "A", "0"); got == nil {
		t.Error("valid request after Hello not answered")
	}
}

func TestStaleNotAnswered(t *testing.T) {
	_, addr := newServer(t, func() *dsmr.Telegram { return nil })
	if got := newBattery(t, addr).poll("HMG-50", "m", "HME-3", Wildcard, "A", "0"); got != nil {
		t.Errorf("answered without a reading: %v", got)
	}
}

func TestSlotsAndAging(t *testing.T) {
	s := New(ctMAC, telegram, slog.New(slog.NewTextHandler(io.Discard, nil)))
	addr := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 22222}
	poll := func(mac, phase, power string, extra ...string) {
		s.handle(req(append([]string{"HMG-50", mac, "HME-3", Wildcard, phase, power}, extra...)...), addr)
	}
	for i := range slotCount + 1 {
		poll(fmt.Sprintf("bat%d", i), "A", "-10")
	}
	if s.find("bat9") != -1 {
		t.Error("tenth battery got a slot")
	}

	s.slots = [slotCount]slot{}
	poll("a1", "A", "-100")
	poll("a2", "A", "40")
	poll("d", "D", "-70")
	poll("x", "0", "500")
	poll("out", "C", "-999", "0")
	a := s.aggregate()
	want := aggregates{
		count:   [buckets]int{bucketA: 2, bucketABC: 1, bucketX: 1},
		charge:  [buckets]int64{bucketA: -100, bucketABC: -70},
		dischrg: [buckets]int64{bucketA: 40, bucketX: 500},
	}
	if a != want {
		t.Errorf("aggregate\n got %+v\nwant %+v", a, want)
	}

	s.age()
	poll("a1", "A", "-100")
	s.age()
	if s.find("a1") < 0 || !s.slots[s.find("a1")].used {
		t.Error("battery polling within 60 s was dropped")
	}
	for _, mac := range []string{"a2", "d", "x", "out"} {
		if i := s.find(mac); i >= 0 && s.slots[i].used {
			t.Errorf("%s still has a slot after 60 s of silence", mac)
		}
	}
}
