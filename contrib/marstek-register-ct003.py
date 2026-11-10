#!/usr/bin/env python3
"""Register dsmr2homewizard's CT003 output as a CT003 in your Marstek account.

A Marstek battery only follows a CT that is in your account and selected in its settings. The
app adds a real CT003 over Bluetooth; this adds one through the same cloud calls the app makes,
so the CT can then be picked in the battery's settings.

    marstek-register-ct003.py list           # log in and show your devices; changes nothing
    marstek-register-ct003.py add <ct-mac>   # add a CT003 with that MAC (dsmr2homewizard's
                                             # -ct003-mac, by default its serial number)

The email comes from MARSTEK_EMAIL or a prompt, the password from MARSTEK_PASSWORD or a hidden
prompt. Set MARSTEK_BASE_URL to https://us.hamedata.com outside Europe.

The API is undocumented; the calls follow what the Marstek app sends, as described by
https://github.com/tomquist/AstraMeter (docs/marstek-mqtt-http.md).
"""

import getpass
import hashlib
import json
import os
import re
import sys
import urllib.parse
import urllib.request

BASE_URL = os.environ.get("MARSTEK_BASE_URL", "https://eu.hamedata.com").rstrip("/")
APP_AGENT = "Dart/2.19 (dart:io)"


def call(path, params, token=None):
    url = f"{BASE_URL}{path}?{urllib.parse.urlencode(params)}"
    headers = {"User-Agent": APP_AGENT, "Accept": "application/json"}
    if token:
        headers["token"] = token
    with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=20) as r:
        return json.loads(r.read().decode("utf-8", "replace"))


def login():
    email = os.environ.get("MARSTEK_EMAIL") or input("Marstek account email: ").strip()
    password = os.environ.get("MARSTEK_PASSWORD") or getpass.getpass("Marstek password: ")
    resp = call("/app/Solar/v2_get_device.php",
                {"mailbox": email, "pwd": hashlib.md5(password.encode()).hexdigest()})
    # code 2: logged in with devices; code 1: logged in, no devices yet.
    if str(resp.get("code")) not in ("1", "2") or not resp.get("token"):
        sys.exit(f"login failed (code={resp.get('code')}): {resp.get('msg')}")
    devices = resp.get("data") if isinstance(resp.get("data"), list) else []
    return email, resp["token"], devices


def show(devices):
    if not devices:
        print("No devices in this account.")
    for d in devices:
        print(f"  {d.get('type') or '?':8}  mac={d.get('mac') or '?':14}  "
              f"devid={d.get('devid') or '?':14}  {d.get('name') or ''}")


def add(mac):
    if not re.fullmatch(r"[0-9a-fA-F]{12}", mac):
        sys.exit(f"{mac!r}: want the 12 hex digits dsmr2homewizard logs as ct_mac")
    email, token, devices = login()
    for d in devices:
        if mac.lower() in (str(d.get("mac", "")).lower(), str(d.get("devid", "")).lower()):
            print("Already in the account:")
            show([d])
            return
    resp = call("/app/Solar/v2_add_device.php", {
        "name": "dsmr2homewizard CT003",
        "mailbox": email,
        "devid": mac,
        "mac": mac,
        "type": "HME-3",
        "token": token,
        "access": "1",
        "bluetooth_name": f"MST-SMR_{mac[-4:]}",  # the app derives the model from this prefix
        "position": "{}",
        "timeZone": "Europe/Amsterdam",
        "version": "122",
    }, token=token)
    if str(resp.get("code")) not in ("1", "2"):
        sys.exit(f"adding the CT003 failed (code={resp.get('code')}): {resp.get('msg')}")
    print("Added. Devices now in the account:")
    show(call_devices(email, token))


def call_devices(email, token):
    # Re-read the list with the same token instead of logging in again.
    resp = call("/ems/api/v1/getDeviceList", {"mailbox": email, "token": token})
    return resp.get("data") if isinstance(resp.get("data"), list) else []


def main():
    if len(sys.argv) == 2 and sys.argv[1] == "list":
        show(login()[2])
    elif len(sys.argv) == 3 and sys.argv[1] == "add":
        add(sys.argv[2])
    else:
        sys.exit(__doc__)


if __name__ == "__main__":
    main()
