// openHAB JS Scripting rule (openhab-js): publish DSMR binding items as a HomeWizard v1 JSON
// snapshot for dsmr2homewizard's MQTT input (-input mqtt -mqtt-topic p1/homewizard).
//
// Install: JS Scripting add-on, an MQTT broker Thing, then copy this file to
// $OPENHAB_CONF/automation/js/. Adjust BROKER, TOPIC and the item names below; leave an entry
// out (or point it at a missing item) when your meter has no such value.

const BROKER = 'mqtt:broker:mosquitto';
const TOPIC = 'p1/homewizard';

// Signed power is delivery minus production, so each power value takes an item pair.
const POWER = {
  active_power_w: ['ElectricityMeterDSMRV50_ActualPowerDelivery', 'MeterActualProduction'],
  active_power_l1_w: ['DSMR_InstantPowerDeliveryL1', 'DSMR_InstantPowerProductionL1'],
  active_power_l2_w: ['DSMR_InstantPowerDeliveryL2', 'DSMR_InstantPowerProductionL2'],
  active_power_l3_w: ['DSMR_InstantPowerDeliveryL3', 'DSMR_InstantPowerProductionL3'],
};

// HomeWizard field -> [item, unit to convert to]; unit null for plain numbers.
const VALUES = {
  total_power_import_t1_kwh: ['DSMR_DeliveryTariff1', 'kWh'],
  total_power_import_t2_kwh: ['DSMR_DeliveryTariff2', 'kWh'],
  total_power_export_t1_kwh: ['DSMR_ProductionTariff1', 'kWh'],
  total_power_export_t2_kwh: ['DSMR_ProductionTariff2', 'kWh'],
  active_voltage_l1_v: ['DSMR_InstantVoltageL1', 'V'],
  active_voltage_l2_v: ['DSMR_InstantVoltageL2', 'V'],
  active_voltage_l3_v: ['DSMR_InstantVoltageL3', 'V'],
  active_current_l1_a: ['DSMR_InstantCurrentL1', 'A'],
  active_current_l2_a: ['DSMR_InstantCurrentL2', 'A'],
  active_current_l3_a: ['DSMR_InstantCurrentL3', 'A'],
  voltage_sag_l1_count: ['DSMR_VoltageSagsL1', null],
  voltage_swell_l1_count: ['DSMR_VoltageSwellsL1', null],
  any_power_fail_count: ['DSMR_PowerFailures', null],
  long_power_fail_count: ['DSMR_LongPowerFailures', null],
  total_gas_m3: ['DSMR_GasMeterValue', 'm³'],
};
const TARIFF_ITEM = 'DSMR_TariffIndicator'; // String "0001"/"0002"
const EQUIPMENT_ITEM = 'DSMR_EquipmentIdentifier'; // String

const COUNTS = new Set(['voltage_sag_l1_count', 'voltage_swell_l1_count',
  'any_power_fail_count', 'long_power_fail_count']);

function read(name, unit) {
  const item = items.getItem(name, true);
  if (item === null || item.isUninitialized) return null;
  if (unit !== null) {
    const q = item.quantityState;
    return q === null ? null : q.toUnit(unit).float;
  }
  return item.numericState;
}

rules.JSRule({
  name: 'P1 to MQTT (HomeWizard v1 snapshot)',
  description: 'Feeds dsmr2homewizard from the DSMR binding',
  triggers: [triggers.ItemStateUpdateTrigger(POWER.active_power_w[0])],
  execute: () => {
    const out = { smr_version: 50 };
    for (const [field, [delivery, production]] of Object.entries(POWER)) {
      const d = read(delivery, 'W');
      const p = read(production, 'W');
      if (d === null && p === null) continue;
      out[field] = Math.round((d ?? 0) - (p ?? 0));
    }
    if (out.active_power_w === undefined) return; // dsmr2homewizard requires it
    for (const [field, [name, unit]] of Object.entries(VALUES)) {
      const v = read(name, unit);
      if (v === null) continue;
      out[field] = COUNTS.has(field) ? Math.round(v) : v;
    }
    const tariff = items.getItem(TARIFF_ITEM, true);
    if (tariff !== null && !tariff.isUninitialized) out.active_tariff = parseInt(tariff.state, 10);
    const id = items.getItem(EQUIPMENT_ITEM, true);
    if (id !== null && !id.isUninitialized) out.unique_id = id.state;

    // Not retained: a retained snapshot would be replayed as fresh after a reconnect.
    actions.thingActions('mqtt', BROKER).publishMQTT(TOPIC, JSON.stringify(out), false);
  },
});
