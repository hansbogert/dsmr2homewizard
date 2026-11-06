// Command dsmr2homewizard reads DSMR P1 telegrams from a serial port, TCP socket, stdin or MQTT
// and serves them through the HomeWizard Energy P1 meter local API (v1 and v2), so devices that
// accept a HomeWizard P1 as their grid meter (home batteries, energy apps) can use any meter.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hansbogert/dsmr2homewizard/internal/dsmr"
	"github.com/hansbogert/dsmr2homewizard/internal/hwapi"
	"github.com/hansbogert/dsmr2homewizard/internal/mdns"
	"github.com/hansbogert/dsmr2homewizard/internal/source"
)

// env lets every flag be set as DSMR2HW_<FLAG_NAME> too, for systemd EnvironmentFile and containers.
func env(name, def string) string {
	if v, ok := os.LookupEnv("DSMR2HW_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))); ok {
		return v
	}
	return def
}

func main() {
	var (
		input      = flag.String("input", env("input", "serial"), "telegram input: serial, tcp, mqtt or stdin")
		serialDev  = flag.String("serial-device", env("serial-device", "/dev/ttyUSB0"), "serial port")
		baud       = flag.Int("serial-baud", atoi(env("serial-baud", "115200")), "baud rate (DSMR 2.2/3: 9600)")
		dataBits   = flag.Int("serial-databits", atoi(env("serial-databits", "8")), "data bits (DSMR 2.2/3: 7)")
		parity     = flag.String("serial-parity", env("serial-parity", "none"), "parity: none, even, odd (DSMR 2.2/3: even)")
		tcpAddr    = flag.String("tcp-addr", env("tcp-addr", ""), "host:port of a raw P1 TCP stream (ser2net)")
		mqttBroker = flag.String("mqtt-broker", env("mqtt-broker", "tcp://localhost:1883"), "MQTT broker URL")
		mqttTopic  = flag.String("mqtt-topic", env("mqtt-topic", "dsmr/telegram"), "MQTT topic carrying raw telegrams")
		mqttUser   = flag.String("mqtt-username", env("mqtt-username", ""), "MQTT username")
		mqttPass   = flag.String("mqtt-password", env("mqtt-password", ""), "MQTT password")
		mqttClient = flag.String("mqtt-client-id", env("mqtt-client-id", "dsmr2homewizard"), "MQTT client ID")

		v1Listen   = flag.String("v1-listen", env("v1-listen", ":80"), "API v1 HTTP listen address; empty disables")
		v2Listen   = flag.String("v2-listen", env("v2-listen", ":443"), "API v2 HTTPS listen address; empty disables")
		stateDir   = flag.String("state-dir", env("state-dir", "/var/lib/dsmr2homewizard"), "directory for serial, v2 tokens and TLS cert")
		serialNo   = flag.String("serial-number", env("serial-number", ""), "12-hex-digit device serial (default: generated once, then persisted)")
		wifiSSID   = flag.String("wifi-ssid", env("wifi-ssid", "dsmr2homewizard"), "Wi-Fi SSID to report")
		maxAge     = flag.Duration("max-age", dur(env("max-age", "15s")), "serve 503 when the newest telegram is older than this; 0 disables")
		pairWindow = flag.Duration("pairing-window", dur(env("pairing-window", "5m")), "accept v2 pairing this long after start and after SIGUSR1")
		noMDNS     = flag.Bool("no-mdns", env("no-mdns", "") != "", "do not advertise over mDNS")
		mdnsIface  = flag.String("mdns-interface", env("mdns-interface", ""), "advertise only on this interface")
		debug      = flag.Bool("debug", env("debug", "") != "", "debug logging")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if err := run(log, config{
		input: *input, serialDev: *serialDev, baud: *baud, dataBits: *dataBits, parity: *parity,
		tcpAddr: *tcpAddr, mqtt: source.MQTT{Broker: *mqttBroker, Topic: *mqttTopic,
			ClientID: *mqttClient, Username: *mqttUser, Password: *mqttPass},
		v1Listen: *v1Listen, v2Listen: *v2Listen, stateDir: *stateDir, serialNo: *serialNo,
		wifiSSID: *wifiSSID, maxAge: *maxAge, pairWindow: *pairWindow,
		mdns: !*noMDNS, mdnsIface: *mdnsIface,
	}); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid integer %q\n", s)
		os.Exit(2)
	}
	return n
}

func dur(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid duration %q\n", s)
		os.Exit(2)
	}
	return d
}

type config struct {
	input, serialDev, parity, tcpAddr string
	baud, dataBits                    int
	mqtt                              source.MQTT
	v1Listen, v2Listen                string
	stateDir, serialNo, wifiSSID      string
	maxAge, pairWindow                time.Duration
	mdns                              bool
	mdnsIface                         string
}

type runner interface {
	Run(ctx context.Context, log *slog.Logger, h source.Handler) error
}

func run(log *slog.Logger, c config) error {
	if c.serialNo != "" && !validSerial(c.serialNo) {
		return fmt.Errorf("serial number %q: want 12 hex digits", c.serialNo)
	}
	var in runner
	switch c.input {
	case "serial":
		in = source.Serial{Device: c.serialDev, Baud: c.baud, DataBits: c.dataBits, Parity: c.parity}
	case "tcp":
		if c.tcpAddr == "" {
			return errors.New("-input tcp needs -tcp-addr")
		}
		in = source.TCP{Addr: c.tcpAddr}
	case "mqtt":
		in = c.mqtt
	case "stdin":
		in = source.Stdin{}
	default:
		return fmt.Errorf("unknown input %q", c.input)
	}

	state, err := hwapi.LoadState(c.stateDir, c.serialNo)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	latest := &hwapi.Latest{MaxAge: c.maxAge}
	dev := hwapi.NewDevice(state, latest, c.wifiSSID, log)
	serial := state.Serial()
	log.Info("starting", "serial", serial, "input", c.input)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	errc := make(chan error, 4)

	var servers []*http.Server
	v1Port, v2Port := 0, 0
	if c.v1Listen != "" {
		ln, err := net.Listen("tcp", c.v1Listen)
		if err != nil {
			return fmt.Errorf("v1 listen: %w", err)
		}
		v1Port = ln.Addr().(*net.TCPAddr).Port
		srv := &http.Server{Handler: dev.V1Handler(), ReadHeaderTimeout: 10 * time.Second}
		servers = append(servers, srv)
		go func() { errc <- srv.Serve(ln) }()
		log.Info("API v1 listening", "addr", ln.Addr())
	}
	if c.v2Listen != "" {
		cert, err := hwapi.LoadOrCreateCert(c.stateDir, serial)
		if err != nil {
			return fmt.Errorf("v2 cert: %w", err)
		}
		ln, err := net.Listen("tcp", c.v2Listen)
		if err != nil {
			return fmt.Errorf("v2 listen: %w", err)
		}
		v2Port = ln.Addr().(*net.TCPAddr).Port
		srv := &http.Server{Handler: dev.V2Handler(), ReadHeaderTimeout: 10 * time.Second,
			TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}}
		servers = append(servers, srv)
		go func() { errc <- srv.ServeTLS(ln, "", "") }()
		log.Info("API v2 listening", "addr", ln.Addr())
		if c.pairWindow > 0 {
			dev.OpenPairing(c.pairWindow)
			usr1 := make(chan os.Signal, 1)
			signal.Notify(usr1, syscall.SIGUSR1)
			go func() {
				for range usr1 {
					dev.OpenPairing(c.pairWindow)
				}
			}()
		}
	}
	if len(servers) == 0 {
		return errors.New("both -v1-listen and -v2-listen are empty")
	}

	if c.mdns {
		var ifaces []net.Interface
		if c.mdnsIface != "" {
			ifc, err := net.InterfaceByName(c.mdnsIface)
			if err != nil {
				return fmt.Errorf("mdns interface: %w", err)
			}
			ifaces = []net.Interface{*ifc}
		}
		stop, err := mdns.Advertise(serial, v1Port, v2Port, ifaces)
		if err != nil {
			return fmt.Errorf("mdns: %w", err)
		}
		defer stop()
	}

	var bad, good atomic.Int64
	go func() {
		errc <- in.Run(ctx, log, func(raw string) {
			t, err := dsmr.Parse(raw)
			if err != nil {
				log.Warn("dropping telegram", "err", err, "dropped_total", bad.Add(1))
				return
			}
			if good.Add(1) == 1 {
				log.Info("first telegram received", "meter", t.Header, "dsmr", t.Version)
			}
			latest.Set(t)
		})
	}()

	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-errc:
		if c.input == "stdin" && err != nil && strings.Contains(err.Error(), "EOF") {
			log.Info("stdin closed; serving until interrupted")
			<-ctx.Done()
			err = ctx.Err()
		}
	}
	sctx, scancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer scancel()
	for _, s := range servers {
		_ = s.Shutdown(sctx)
	}
	return err
}

func validSerial(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, r := range strings.ToLower(s) {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
