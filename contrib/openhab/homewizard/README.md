# openHAB reading dsmr2homewizard

For setups where dsmr2homewizard owns the P1 serial port and openHAB keeps its existing DSMR-era
items. Tested on openHAB 3.4.5. openHAB reads the emulator through the HomeWizard binding, and
`homewizard-split.rules` copies the values into the old items:

- signed `active_power` becomes `…ActualPowerDelivery` / `…ActualPowerProduction`, same for L1;
- the tariff counters and gas are copied one to one.

Install:

1. `conf/services/addons.cfg`: add `homewizard` to `binding` and **remove `dsmr`**. Otherwise the
   DSMR binding competes for the serial port, and whichever opens it first after a reboot wins.
2. Copy the `.things`, `.items` and `.rules` files into `conf/things`, `conf/items` and
   `conf/rules`. Set `ipAddress` to the emulator's host (`127.0.0.1` when openHAB uses host
   networking on the same machine), and rename the target items in the rule to yours.

The 3.4 binding has no voltage, current or failure-counter channels.
