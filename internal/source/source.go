// Package source delivers raw DSMR telegrams from a serial port, a TCP socket (ser2net and
// friends), stdin, or an MQTT topic.
package source

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"go.bug.st/serial"

	"github.com/hansbogert/dsmr2homewizard/internal/dsmr"
)

// Handler receives each raw telegram; it does the parsing so the CRC is checked once.
type Handler func(raw string)

// Serial reads telegrams from a serial port. DSMR 4/5 meters use 115200 8N1; DSMR 2.2/3
// meters use 9600 7E1 (parity "even", 7 data bits).
type Serial struct {
	Device   string
	Baud     int
	DataBits int
	Parity   string // "none", "even", "odd"
}

func (s Serial) Run(ctx context.Context, log *slog.Logger, h Handler) error {
	parity := map[string]serial.Parity{"none": serial.NoParity, "even": serial.EvenParity,
		"odd": serial.OddParity}[strings.ToLower(s.Parity)]
	mode := &serial.Mode{BaudRate: s.Baud, DataBits: s.DataBits, Parity: parity,
		StopBits: serial.OneStopBit}
	return stream(ctx, log, "serial "+s.Device, func() (io.ReadCloser, error) {
		return serial.Open(s.Device, mode)
	}, h)
}

// TCP reads telegrams from a TCP socket, e.g. ser2net or a P1-to-Wi-Fi dongle in raw mode.
type TCP struct{ Addr string }

func (t TCP) Run(ctx context.Context, log *slog.Logger, h Handler) error {
	return stream(ctx, log, "tcp "+t.Addr, func() (io.ReadCloser, error) {
		var d net.Dialer
		dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return d.DialContext(dctx, "tcp", t.Addr)
	}, h)
}

// Stdin reads telegrams from standard input and returns at EOF.
type Stdin struct{}

func (Stdin) Run(ctx context.Context, log *slog.Logger, h Handler) error {
	r := dsmr.NewReader(os.Stdin)
	for ctx.Err() == nil {
		raw, err := r.Next()
		if err != nil {
			return err
		}
		h(raw)
	}
	return ctx.Err()
}

// stream opens a byte stream, frames telegrams from it, and reopens it with backoff when it
// fails. A stream that never yields data (wrong baud rate, unplugged cable) is cut after
// idleTimeout so it gets reopened.
func stream(ctx context.Context, log *slog.Logger, name string, open func() (io.ReadCloser, error), h Handler) error {
	const idleTimeout = 60 * time.Second
	backoff := time.Second
	for ctx.Err() == nil {
		rc, err := open()
		if err != nil {
			log.Warn("open input failed", "input", name, "err", err, "retry_in", backoff)
		} else {
			log.Info("input connected", "input", name)
			got := readAll(ctx, rc, idleTimeout, h)
			if got {
				backoff = time.Second
			}
			log.Warn("input lost", "input", name, "retry_in", backoff)
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
	return ctx.Err()
}

// readAll frames telegrams from rc until it errors, goes idle, or ctx ends. It reports
// whether at least one telegram arrived.
func readAll(ctx context.Context, rc io.ReadCloser, idle time.Duration, h Handler) bool {
	defer rc.Close()
	frames := make(chan string)
	errc := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		r := dsmr.NewReader(rc)
		for {
			raw, err := r.Next()
			if err != nil {
				errc <- err
				return
			}
			select {
			case frames <- raw:
			case <-done:
				return
			}
		}
	}()
	got := false
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		select {
		case raw := <-frames:
			got = true
			h(raw)
			timer.Reset(idle)
		case <-errc:
			return got
		case <-timer.C:
			return got
		case <-ctx.Done():
			return got
		}
	}
}

// MQTT subscribes to a topic whose payload is a raw DSMR telegram, as published by e.g.
// DSMR-reader, ser2mqtt or an ESPHome/Tasmota P1 bridge configured for raw telegrams.
type MQTT struct {
	Broker   string // tcp://host:1883, ssl://host:8883, ws://...
	Topic    string
	ClientID string
	Username string
	Password string
}

func (m MQTT) Run(ctx context.Context, log *slog.Logger, h Handler) error {
	opts := mqtt.NewClientOptions().
		AddBroker(m.Broker).
		SetClientID(m.ClientID).
		SetUsername(m.Username).
		SetPassword(m.Password).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetMaxReconnectInterval(30 * time.Second).
		SetOrderMatters(false)
	onMsg := func(_ mqtt.Client, msg mqtt.Message) { h(string(msg.Payload())) }
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		log.Info("mqtt connected", "broker", m.Broker, "topic", m.Topic)
		// Resubscribe on every (re)connect; clean sessions drop subscriptions.
		if t := c.Subscribe(m.Topic, 0, onMsg); t.Wait() && t.Error() != nil {
			log.Error("mqtt subscribe failed", "topic", m.Topic, "err", t.Error())
		}
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		log.Warn("mqtt connection lost", "err", err)
	})
	c := mqtt.NewClient(opts)
	if t := c.Connect(); t.Wait() && t.Error() != nil {
		return fmt.Errorf("mqtt connect: %w", t.Error())
	}
	<-ctx.Done()
	c.Disconnect(250)
	return ctx.Err()
}
