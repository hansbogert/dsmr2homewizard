// Package mdns advertises the emulated meter the way a real HomeWizard P1 meter does.
package mdns

import (
	"net"
	"strings"

	"github.com/grandcat/zeroconf"
)

// Advertise registers _hwenergy._tcp (API v1) and, when v2Port > 0, _homewizard._tcp (API v2).
// A port of 0 skips that service. ifaces limits the interfaces used (nil = all).
func Advertise(serial string, v1Port, v2Port int, ifaces []net.Interface) (stop func(), err error) {
	instance := "p1meter-" + strings.ToUpper(serial[len(serial)-6:])
	var servers []*zeroconf.Server
	stop = func() {
		for _, s := range servers {
			s.Shutdown()
		}
	}
	if v1Port > 0 {
		s, err := zeroconf.Register(instance, "_hwenergy._tcp", "local.", v1Port, []string{
			"api_enabled=1", "path=/api/v1", "serial=" + serial,
			"product_name=P1 Meter", "product_type=HWE-P1",
		}, ifaces)
		if err != nil {
			return stop, err
		}
		servers = append(servers, s)
	}
	if v2Port > 0 {
		s, err := zeroconf.Register(instance, "_homewizard._tcp", "local.", v2Port, []string{
			"api_version=2.0.0", "id=appliance/p1dongle/" + serial, "serial=" + serial,
			"product_name=P1 Meter", "product_type=HWE-P1",
		}, ifaces)
		if err != nil {
			stop()
			return func() {}, err
		}
		servers = append(servers, s)
	}
	return stop, nil
}
