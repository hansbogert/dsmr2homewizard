package ct003

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hansbogert/dsmr2homewizard/internal/dsmr"
)

const (
	slotCount = 9                // batteries a CT003 serves at once; the 10th is not answered
	ageStep   = 30 * time.Second // slots age one step per 30 s and are dropped at age 2
	rssi      = -50              // reported Wi-Fi signal, dBm
)

// Server answers battery requests like a CT003 (firmware v122).
type Server struct {
	mac   string
	get   func() *dsmr.Telegram
	log   *slog.Logger
	Pause time.Duration // wait after each reply, as the CT003 does; at most ~2 replies/s overall

	mu    sync.Mutex
	slots [slotCount]slot
	next  int   // round-robin position of the reply loop
	idx   uint8 // info_idx of the last reply
	wake  chan struct{}
}

type slot struct {
	used    bool
	addr    net.Addr
	req     request
	age     int
	waiting bool // a request arrived since the last reply
}

// New returns a CT003 with the given MAC that reports the telegram get returns. A nil telegram
// (no data, or stale) means requests are not answered, so batteries see the meter go offline
// and hold 0 W instead of steering on an old reading.
func New(mac string, get func() *dsmr.Telegram, log *slog.Logger) *Server {
	return &Server{mac: mac, get: get, log: log, Pause: 500 * time.Millisecond,
		wake: make(chan struct{}, 1)}
}

// Serve answers requests on conn until ctx ends, then closes conn.
func (s *Server) Serve(ctx context.Context, conn net.PacketConn) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	go s.ageLoop(ctx)
	go s.replyLoop(ctx, conn)

	buf := make([]byte, 1500)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			s.log.Warn("ct003: read", "err", err)
			continue
		}
		s.handle(buf[:n], addr)
	}
}

func (s *Server) handle(b []byte, addr net.Addr) {
	if bytes.HasPrefix(b, []byte("Hello")) {
		return // the CT003 keeps these for diagnostics and never answers them
	}
	r, err := parseRequest(b)
	if err != nil {
		s.log.Debug("ct003: dropping request", "from", addr, "err", err)
		return
	}
	// The CT compares the type over the request's length, so it is a prefix match.
	if !strings.HasPrefix(Type, r.ctType) {
		s.log.Debug("ct003: ignoring request for another CT type", "from", addr, "type", r.ctType)
		return
	}
	if r.ctMAC != Wildcard && r.ctMAC != s.mac {
		s.log.Debug("ct003: ignoring request for another CT", "from", addr, "ct_mac", r.ctMAC,
			"ours", s.mac)
		return
	}

	s.mu.Lock()
	i := s.find(r.devMAC)
	if i < 0 {
		s.mu.Unlock()
		s.log.Warn("ct003: all slots taken, not answering new battery", "battery", r.devMAC,
			"from", addr)
		return
	}
	sl := &s.slots[i]
	if !sl.used {
		s.log.Info("ct003: battery connected", "battery", r.devMAC, "type", r.devType,
			"from", addr, "phase", string(r.phase))
	} else if sl.req.phase != r.phase {
		s.log.Info("ct003: battery changed phase", "battery", r.devMAC,
			"from_phase", string(sl.req.phase), "to_phase", string(r.phase))
	}
	*sl = slot{used: true, addr: addr, req: r, waiting: true}
	s.mu.Unlock()
	s.log.Debug("ct003: request", "battery", r.devMAC, "phase", string(r.phase),
		"power", r.power, "participate", r.participate)

	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// find returns the battery's slot, else a free one, else -1. s.mu must be held.
func (s *Server) find(mac string) int {
	free := -1
	for i := range s.slots {
		switch {
		case s.slots[i].used && s.slots[i].req.devMAC == mac:
			return i
		case !s.slots[i].used && free < 0:
			free = i
		}
	}
	return free
}

// ageLoop drops batteries that stop polling: 30–60 s after their last request.
func (s *Server) ageLoop(ctx context.Context) {
	t := time.NewTicker(ageStep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.age()
		}
	}
}

func (s *Server) age() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.slots {
		sl := &s.slots[i]
		if !sl.used {
			continue
		}
		if sl.age++; sl.age >= 2 {
			s.log.Info("ct003: battery gone", "battery", sl.req.devMAC)
			*sl = slot{}
		}
	}
}

// replyLoop answers waiting slots one at a time in slot order, pausing after each reply.
func (s *Server) replyLoop(ctx context.Context, conn net.PacketConn) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
		for {
			pkt, addr := s.nextReply()
			if pkt == nil {
				break
			}
			s.log.Debug("ct003: reply", "to", addr, "packet", string(pkt))
			if _, err := conn.WriteTo(pkt, addr); err != nil && ctx.Err() == nil {
				s.log.Warn("ct003: reply", "to", addr, "err", err)
			}
			if s.Pause > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(s.Pause):
				}
			}
		}
	}
}

// nextReply builds the reply for the next waiting slot, or returns nil when none is waiting or
// there is no current reading (then the waiting requests are dropped unanswered).
func (s *Server) nextReply() ([]byte, net.Addr) {
	t := s.get()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range slotCount {
		i := (s.next + k) % slotCount
		sl := &s.slots[i]
		if !sl.used || !sl.waiting {
			continue
		}
		if t == nil {
			for j := range s.slots {
				s.slots[j].waiting = false
			}
			s.log.Debug("ct003: no current reading, not answering")
			return nil, nil
		}
		sl.waiting = false
		s.next = i + 1
		pkt := frame(s.replyBody(sl.req, readingOf(t), s.aggregate()))
		s.idx++ // the first reply carries 0, as AstraMeter's does
		return pkt, sl.addr
	}
	return nil, nil
}

// reading is what the CT003 measures.
type reading struct {
	phase [3]int64 // W per phase, negative when exporting
	total int64    // W
}

func readingOf(t *dsmr.Telegram) reading {
	var r reading
	for p := range 3 {
		r.phase[p] = watts(t.PhaseDelivered[p], t.PhaseReturned[p])
	}
	if t.PowerDelivered != nil || t.PowerReturned != nil {
		r.total = watts(t.PowerDelivered, t.PowerReturned)
	} else {
		r.total = r.phase[0] + r.phase[1] + r.phase[2]
	}
	return r
}

// watts returns delivered minus returned, from kW to W; 0 when neither was sent.
func watts(delivered, returned *float64) int64 {
	var kw float64
	if delivered != nil {
		kw += *delivered
	}
	if returned != nil {
		kw -= *returned
	}
	return int64(math.Round(kw * 1000))
}

// Buckets in the order the reply carries them: unassigned (still detecting), A, B, C, and the
// combined bucket of batteries balancing the sum of all phases (request phase 'D').
const (
	bucketX = iota
	bucketA
	bucketB
	bucketC
	bucketABC
	buckets
)

type aggregates struct {
	count           [buckets]int // bucketX has no count on the wire
	charge, dischrg [buckets]int64
}

// aggregate sums the participating batteries' reported power per bucket, negative (charging)
// and positive (discharging) separately. s.mu must be held.
func (s *Server) aggregate() aggregates {
	var a aggregates
	for _, sl := range s.slots {
		if !sl.used || !sl.req.participate {
			continue
		}
		b := bucketX
		switch sl.req.phase {
		case 'A':
			b = bucketA
		case 'B':
			b = bucketB
		case 'C':
			b = bucketC
		case 'D':
			b = bucketABC
		}
		a.count[b]++
		if p := int64(sl.req.power); p < 0 {
			a.charge[b] += p
		} else {
			a.dischrg[b] += p
		}
	}
	return a
}

// replyBody lays out the 24 reply fields AstraMeter sends. The CT003's four trailing energy
// counters are left out: how the real CT fills them is untraced, and sending them coincided
// with a Venus E 3.0 rebooting every 10–60 minutes. s.mu must be held.
func (s *Server) replyBody(r request, m reading, a aggregates) string {
	f := []string{Type, s.mac, r.devType, r.devMAC}
	num := func(v int64) { f = append(f, strconv.FormatInt(v, 10)) }
	for _, p := range m.phase {
		num(clamp(p, math.MinInt32, math.MaxInt32))
	}
	num(clamp(m.total, math.MinInt32, math.MaxInt32))
	for _, b := range []int{bucketA, bucketB, bucketC, bucketABC} {
		num(int64(min(a.count[b], math.MaxUint8)))
	}
	num(rssi)
	num(int64(s.idx))
	for b := range buckets {
		num(clamp(a.charge[b], math.MinInt16, math.MaxInt16))
	}
	for b := range buckets {
		num(clamp(a.dischrg[b], math.MinInt16, math.MaxInt16))
	}
	return "|" + strings.Join(f, "|")
}
