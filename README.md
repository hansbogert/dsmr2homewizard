# dsmr2homewizard

Emulates a **HomeWizard P1 meter** (`HWE-P1`) from any DSMR smart meter feed, so that devices
which accept a HomeWizard P1 as their grid meter — home batteries (Zendure, Marstek, …), energy
apps, Home Assistant's `homewizard` integration — can use a meter you already read some other way.

Inputs:

| `-input`  | Source                                                                 |
|-----------|------------------------------------------------------------------------|
| `serial`  | P1 cable on a serial port (default `/dev/ttyUSB0`, 115200 8N1)         |
| `tcp`     | raw P1 byte stream over TCP (ser2net, P1-to-Wi-Fi dongles in raw mode) |
| `mqtt`    | MQTT topic whose payload is one raw telegram                           |
| `stdin`   | telegrams on standard input (testing)                                  |

Every telegram's CRC is checked (DSMR 4+); bad telegrams are dropped and logged. MQTT payloads
that lost their CRLF line endings on the way are repaired before the check.

Outputs:

- **API v1** on HTTP `:80`: `GET /api`, `GET /api/v1/data`, `GET /api/v1/telegram`,
  `GET|PUT /api/v1/system`, `PUT /api/v1/identify`.
- **API v2** on HTTPS `:443`: `GET /api`, `POST|DELETE /api/user`, `GET /api/measurement`,
  `GET /api/telegram`, `GET|PUT /api/system`, `PUT /api/identify`.
- **mDNS**: `_hwenergy._tcp` (v1) and `_homewizard._tcp` (v2), with the TXT records of a real
  meter, as `p1meter-XXXXXX`.

## Prior art

Checked before writing this (2026-11):
[wmnl25/homewizard-p1-emulator-addon](https://github.com/wmnl25/homewizard-p1-emulator-addon)
(Home Assistant add-on, reads HA sensor entities),
[wmnl25/homewizard-p1-emulator-python](https://github.com/wmnl25/homewizard-p1-emulator-python)
(polls HA sensors, v1 `/api/v1/data` only), and
[jeroen-klaver/Homewizard-emulator](https://github.com/jeroen-klaver/Homewizard-emulator)
(random data). None reads DSMR directly from serial or MQTT, or runs without Home Assistant.

## Fail-safe on stale data

If no valid telegram arrived within `-max-age` (default 15s), the data, measurement and
telegram endpoints answer **503** instead of the last value. A battery steering on grid power
should see an outage, not a frozen reading. `-max-age 0` disables this.

## Build and run

```sh
go build -o dsmr2homewizard ./cmd/dsmr2homewizard

# P1 cable, DSMR 4/5
sudo ./dsmr2homewizard -input serial -serial-device /dev/ttyUSB0

# DSMR 2.2 / 3 meters
sudo ./dsmr2homewizard -serial-baud 9600 -serial-databits 7 -serial-parity even

# raw telegrams on MQTT
sudo ./dsmr2homewizard -input mqtt -mqtt-broker tcp://broker:1883 -mqtt-topic dsmr/telegram
```

Every flag can also be set as an environment variable `DSMR2HW_<FLAG>` (e.g.
`DSMR2HW_MQTT_BROKER`); see `-h`. Ports 80/443 are what consumers expect; most clients cannot be
pointed elsewhere.

### systemd

`contrib/dsmr2homewizard.service` runs it unprivileged (`DynamicUser`, `CAP_NET_BIND_SERVICE`,
`dialout` group, state in `/var/lib/dsmr2homewizard`). Configure via
`/etc/default/dsmr2homewizard`.

## API v2 pairing

A real meter accepts `POST /api/user` only after its button is pressed. Here, pairing is open for
`-pairing-window` (default 5 minutes) after start, and again for the same duration after
`SIGUSR1` (`systemctl reload dsmr2homewizard`). Tokens persist in `state.json`.

v2 uses a **self-signed** certificate, generated once into the state directory. Real meters
present a certificate from HomeWizard's private CA, so clients that validate against that CA
(the official HomeWizard app, strict libraries) will reject the emulator over v2 and must use
v1. The v2 WebSocket (`/api/ws`) and battery-control endpoints are not implemented.

## Identity

The 12-hex-digit serial (also the "MAC" clients see) is generated once and stored in
`state.json`. Set `-serial-number` to pin it, e.g. when replacing a real meter whose serial a
consumer is already paired with.

## Field mapping notes

- Power and current are signed: negative when exporting. DSMR sends currents unsigned; the sign
  comes from the phase's net power.
- Fields the meter does not send are omitted, as on the real device. `wifi_ssid`/`wifi_strength`
  are reported as configured/100 since some clients expect them.
- `meter_model` is the telegram's identification line (e.g. `ISK5\2M550T-1012`).
- Belgian meters: `0-0:96.1.4` maps to `smr_version` (50217 → 50); capacity-tariff fields
  (`1-0:1.4.0`, `1-0:1.6.0`) map to `active_power_average_w` / `montly_power_peak_w` (v1
  spelling) and `average_power_15m_w` / `monthly_power_peak_w` (v2). HomeWizard's own T1/T2 swap
  for Belgium is not replicated.
- DSMR 2.2 gas (`0-n:24.3.0`, value on the next line) is not parsed.
