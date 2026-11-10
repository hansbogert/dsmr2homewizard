# dsmr2homewizard

Emulates a **HomeWizard P1 meter** (`HWE-P1`) from any DSMR smart meter feed, so that devices
which accept a HomeWizard P1 as their grid meter — home batteries (Zendure, Marstek, …), energy
apps, Home Assistant's `homewizard` integration — can use a meter you already read some other way.

Inputs:

| `-input`  | Source                                                                 |
|-----------|------------------------------------------------------------------------|
| `serial`  | P1 cable on a serial port (default `/dev/ttyUSB0`, 115200 8N1)         |
| `tcp`     | raw P1 byte stream over TCP (ser2net, P1-to-Wi-Fi dongles in raw mode) |
| `mqtt`    | MQTT topic: one raw telegram, or a JSON snapshot (below) per message   |
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
- **Marstek CT003** on UDP `:12345`, opt-in with `-ct003-listen :12345`; see below.

## Prior art

Checked before writing this (2026-11):
[wmnl25/homewizard-p1-emulator-addon](https://github.com/wmnl25/homewizard-p1-emulator-addon)
(Home Assistant add-on, reads HA sensor entities),
[wmnl25/homewizard-p1-emulator-python](https://github.com/wmnl25/homewizard-p1-emulator-python)
(polls HA sensors, v1 `/api/v1/data` only), and
[jeroen-klaver/Homewizard-emulator](https://github.com/jeroen-klaver/Homewizard-emulator)
(random data). None reads DSMR directly from serial or MQTT, or runs without Home Assistant.

For the CT003 output:
[tomquist/AstraMeter](https://github.com/tomquist/AstraMeter) (Python, GPL-3.0) emulates a
CT002/CT003 from many sources but has no DSMR reader, and does not send the CT003's energy
counters. Its [protocol notes](https://github.com/tomquist/AstraMeter/blob/main/docs/ct002-ct003-protocol.md)
are what this implementation follows; no code is taken from it.

## JSON snapshots (openHAB and other parsed sources)

Some publishers have only parsed values, for example openHAB's DSMR binding, which never exposes
the raw telegram. They can publish one JSON object per reading instead, using the HomeWizard v1
field names from `/api/v1/data`. Power and current are signed, negative when exporting:

```json
{"active_power_w": -412, "active_power_l1_w": -412, "active_current_l1_a": 1.8,
 "active_voltage_l1_v": 229.4, "total_power_import_t1_kwh": 1234.5, "active_tariff": 1}
```

- `active_power_w` is required. Every other field is optional.
- Unknown fields are rejected, so a typo shows up in the log instead of as a missing value.
- `/api/v1/telegram` and `/api/telegram` answer 404 for this input.
- Retained MQTT messages are ignored for both formats.

`contrib/openhab/p1-to-mqtt.js` is a JS Scripting rule that publishes this snapshot on every
DSMR update. Adjust its broker Thing UID, topic and item names.

## Fail-safe on stale data

If no valid telegram arrived within `-max-age` (default 15s), the data, measurement and
telegram endpoints answer **503** instead of the last value. A battery steering on grid power
should see an outage, not a frozen reading. `-max-age 0` disables this. The CT003 output does
not answer at all then, which is what a battery sees when its CT drops off the network.

## Marstek CT003

With `-ct003-listen :12345` the program also answers like a Marstek **CT003** (`HME-3`), the P1
meter Marstek batteries (Venus, B2500, Jupiter) read the grid from. Set the battery's meter type
to CT003. It behaves like the real CT003 (firmware v122), not like a controller:

- Each battery polls over UDP and gets its own reply with the per-phase and total grid power,
  how many batteries sit on each phase, the charge/discharge power the batteries reported per
  phase, and the energy registers `1-0:1.8.1`, `1.8.2`, `2.8.1`, `2.8.2` (0.1 Wh). The batteries
  divide the load among themselves, as with the real meter.
- It answers requests for CT MAC `000000000000` (an unbound battery, which then binds to this
  one) or for its own MAC, `-ct003-mac`, by default the serial number. The battery compares the
  MAC byte for byte. To replace a real CT003 without re-pairing, set `-ct003-mac` to its MAC in
  exactly the form the battery uses.
- At most 9 batteries; one that stops polling is dropped after 30 to 60 s. Replies go out at
  most about two per second in total, 500 ms apart, as the real CT003 paces them.
- Batteries reach the CT on the LAN, possibly by broadcast, so use host networking in Docker. Single-phase
  meters report L2 and L3 as 0.

Not implemented: the CT003's MQTT/cloud reporting to the Marstek app and its BLE side, so the
app will not show this meter.

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

### Docker Compose

`contrib/docker-compose.yml` runs it with host networking (ports 80/443 and mDNS), the P1 cable
by its stable `/dev/serial/by-id/…` name, and state in `./state`. Copy it next to the `Dockerfile`
and the built binary, adjust the device path, then:

```sh
docker compose up -d --build
docker compose logs -f
```

`restart: always` only covers a container that has been started once; a merely created one
stays down, even across reboots.

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
(the official HomeWizard app, Home Assistant, openHAB, ioBroker) reject the emulator over v2
and must use v1. `/api/batteries` reports no batteries; the v2 WebSocket (`/api/ws`) and battery
control are not implemented.

## Identity

A HomeWizard serial is the device's MAC. On first start the serial is the MAC of the interface
carrying the default route (or `-mdns-interface`), and is then kept in `state.json`. Home
Assistant uses it to follow the meter across DHCP address changes. Set `-serial-number` to pin
it, e.g. when replacing a real meter whose serial a consumer is already paired with.

mDNS advertises only that interface's addresses, under host name `p1meter-<last 6 of serial>`.

## Client compatibility

Checked against client source code (2026-11):

| Client | API | Notes |
|---|---|---|
| Home Assistant (`homewizard`) | v1 | v2 impossible: CA pinned. It picks v2 only if HTTPS `GET /api` answers 401, so the emulator answers 200 there. It probes `/api/v1/state` and needs a 404. Gas comes only from `external`. |
| evcc | v1 | Requires `api_version: "v1"`. Energy is the sum of the `t1..t4` registers. |
| SmartEVSE | v1 | Finds the meter by mDNS host name prefix `p1meter-`. Reads `active_current_a` on single-phase meters. Stops charging after 11s without data. |
| openHAB | v1 | Requests `/api/` (trailing slash) and `/api/v1/system` on every poll. Any non-200 means offline. v2 impossible: CA pinned. |
| Homey | v1, v2 | v2 polls `/api/batteries` together with the measurement. Homey accepts any certificate. |
| Victron (dbus drivers) | v1 | `back2basic` publishes **0 W** when it gets an error such as the stale-data 503. Its 3-phase mode expects every L1–L3 field to be present. |
| Zendure, Marstek | ? | Closed firmware. Zendure reportedly uses v1 on port 80. |

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
