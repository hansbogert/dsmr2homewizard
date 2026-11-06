// Package mdns advertises the emulated meter the way a real HomeWizard P1 meter does.
package mdns

import (
	"errors"
	"net"
	"os"
	"strings"

	"github.com/grandcat/zeroconf"
)

// Advertise registers _hwenergy._tcp (API v1) and, when v2Port > 0, _homewizard._tcp (API v2).
// A port of 0 skips that service. ifaces limits the interfaces used (nil = all up interfaces).
//
// The SRV target is the host name "p1meter-XXXXXX", not the machine's own name: SmartEVSE and
// Homey classify a discovered meter by that host name prefix and connect to it by name.
func Advertise(serial string, v1Port, v2Port int, ifaces []net.Interface) (stop func(), err error) {
	if ifaces == nil {
		if ifaces, err = net.Interfaces(); err != nil {
			return func() {}, err
		}
	}
	ips := addresses(ifaces)
	if len(ips) == 0 {
		return func() {}, errors.New("no usable IP addresses to advertise")
	}
	host := "p1meter-" + strings.ToLower(serial[len(serial)-6:])
	instance := "p1meter-" + strings.ToUpper(serial[len(serial)-6:])
	common := []string{"serial=" + serial, "product_name=P1 meter", "product_type=HWE-P1"}

	var servers []*zeroconf.Server
	stop = func() {
		for _, s := range servers {
			s.Shutdown()
		}
	}
	register := func(service string, port int, txt []string) error {
		s, err := zeroconf.RegisterProxy(instance, service, "local.", port, host, ips,
			append(txt, common...), ifaces)
		if err == nil {
			servers = append(servers, s)
		}
		return err
	}
	if v1Port > 0 {
		if err := register("_hwenergy._tcp", v1Port, []string{"api_enabled=1", "path=/api/v1"}); err != nil {
			stop()
			return func() {}, err
		}
	}
	if v2Port > 0 {
		if err := register("_homewizard._tcp", v2Port, []string{"api_version=2.0.0",
			"id=appliance/p1dongle/" + serial}); err != nil {
			stop()
			return func() {}, err
		}
	}
	return stop, nil
}

// addresses returns the IPv4 and global IPv6 addresses of the up, multicast-capable,
// non-loopback interfaces among ifaces.
func addresses(ifaces []net.Interface) []string {
	var ips []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 ||
			ifc.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.IsLinkLocalUnicast() {
				continue
			}
			ips = append(ips, n.IP.String())
		}
	}
	return ips
}

// MAC returns the lowercase, colon-less hardware address of the first up, non-loopback
// interface among ifaces (nil = all), or "" if there is none. HomeWizard serials are the
// device's MAC; Home Assistant matches DHCP announcements to a paired meter by it.
func MAC(ifaces []net.Interface) string {
	if ifaces == nil {
		ifaces, _ = net.Interfaces()
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || len(ifc.HardwareAddr) != 6 {
			continue
		}
		return strings.ReplaceAll(ifc.HardwareAddr.String(), ":", "")
	}
	return ""
}

// DefaultInterface returns the interface carrying the IPv4 default route (Linux), or nil when
// it cannot be determined. Advertising on every interface would also announce Docker bridge
// and VPN addresses that clients on the LAN cannot reach.
func DefaultInterface() *net.Interface {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		// Iface Destination Gateway Flags ...; destination 00000000 is the default route.
		if len(f) > 2 && f[1] == "00000000" {
			if ifc, err := net.InterfaceByName(f[0]); err == nil {
				return ifc
			}
		}
	}
	return nil
}
