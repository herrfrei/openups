# OpenUPSd

Reads a Mini-Box USB UPS directly over USB and makes it available to Home
Assistant through **MQTT discovery** and a **read-only NUT server** on TCP 3493.

## Before you start

1. **Stop any other NUT server.** The NUT add-on (or a system `upsd`) claims the
   same USB interface and the same TCP port 3493. Only one of them can run.
2. **A running MQTT broker** is needed for the entities. The default settings
   expect the Mosquitto add-on and its Home Assistant MQTT integration.
3. The UPS must be plugged into the Home Assistant machine. OpenUPS is detected
   automatically; other models must be chosen by hand (see *Supported devices*).

## Quick start

1. Install and start the add-on with the default options.
2. Open the **Log** tab. A working start shows `usb: OpenUPS opened` and
   `mqtt: connected`.
3. In **Settings → Devices & services → MQTT** a device with the UPS entities
   appears on its own.

## Supported devices

| Model | `model` value | Status |
|---|---|---|
| OpenUPS (`04d8:d004`) | `openups` | Verified on real hardware, auto-detected |
| OpenUPS2 (`04d8:d005`) | `openups2` | Experimental |
| NUC-UPS (`04d8:d007`) | `nucups` | Experimental |
| DCDC-USB (`04d8:d003`) | `dcdcusb` | Experimental |

With `model: auto` only the OpenUPS is detected. The other models are never
auto-detected, because their byte offsets are unverified and a wrong offset
gives plausible but wrong numbers. Selecting one logs a warning at startup.

## Options

| Option | Default | Description |
|---|---|---|
| `model` | `auto` | `auto`, `openups`, `openups2`, `nucups` or `dcdcusb`. |
| `ups_name` | empty | Name of the UPS in NUT. Empty uses the model default. |
| `mqtt_host` | `core-mosquitto` | MQTT broker host name. |
| `mqtt_port` | `1883` | MQTT broker port. |
| `mqtt_username` | empty | MQTT user name, if your broker needs one. |
| `mqtt_password` | empty | MQTT password. |
| `mqtt_prefix` | `homeassistant` | Discovery prefix; must match the MQTT integration. |
| `node_id` | `openups` | Used in MQTT topics and in every entity's unique ID. |
| `nut_listen` | `:3493` | NUT listen address. |
| `interval` | `5` | Poll interval in seconds (1 to 3600). |
| `discovery` | `true` | Publish Home Assistant discovery messages. |
| `device_path` | empty | Explicit device node such as `/dev/bus/usb/003/009`. |
| `device_index` | `0` | Which device to use when several match. |
| `battery_estimate` | `true` | Estimate battery charge and runtime (OpenUPS only). |

**`node_id`:** Home Assistant entity unique IDs are built from it. Changing it
later creates new entities and leaves the old ones orphaned, so pick it once.

**`device_path`:** bus and device numbers change after a replug or a reboot.
Leave it empty unless auto-detection cannot find the device.

## Entities

For the OpenUPS the add-on creates these sensors:

| Entity | Unit | Notes |
|---|---|---|
| VIN | V | Input voltage |
| VOut | V | Output voltage |
| VBat | V | Battery voltage |
| Input Current | A | The load drawn from the input while on mains |
| Charge Current | A | Battery charge current |
| Discharge Current | A | Battery discharge current |
| Cell 1 to 6 Voltage | V | Per-cell voltage |
| Temperature | °C | |
| Output Power | W | |
| Power State | | `battery`, `charging` or `idle` |
| Firmware | | Diagnostic entity |
| Battery Charge | % | **Estimate**, see below |
| Runtime Estimate | s | **Estimate**: how long the battery would last at the current load |
| Charge Estimate | | Diagnostic: `tracking` or `initial, from voltage` |

Value entities become `unknown` within a minute if the add-on stops, so a dead
add-on is visible in Home Assistant instead of showing stale numbers.

## Battery charge and runtime (estimates)

The OpenUPS does not report a charge percentage or a runtime, so the add-on
works them out. At start-up it reads the battery description stored in the UPS
(capacity, cell count, cutoff voltage and the voltage-to-charge curve) and then:

* **counts charge** in and out from the measured battery currents;
* **corrects the count** only when the voltage can be trusted: at the end of a
  full charge (100 %), when the cells reach the cutoff voltage on battery (0 %),
  and when the battery rests at a low charge, where the voltage curve is steep;
* **estimates the runtime** as remaining capacity divided by the discharge
  current when on battery, or by the load power (with an efficiency learned
  during outages, 90 % until then) when on mains.

This is an estimate and it can be wrong, mainly in these situations:

* A LiFePO4 battery has an almost flat voltage curve between about 25 % and
  75 %. Right after the first start the charge comes from the voltage alone
  (`Charge Estimate` shows `initial, from voltage`) and can be off by a lot.
  It becomes reliable after the battery has been fully charged once while the
  add-on was running, which turns the state to `tracking`.
* The count is saved in `/data` once a minute, and restored after a restart if it
  is less than an hour old. After a longer pause it starts from the voltage again.
* The runtime assumes the current load stays the same. It is capped at 48 hours
  and not shown at all when the load is below about 1 W.
* Nothing here is a low-battery alarm. The NUT status never reports `LB`, so
  clients will not shut down because of this estimate.

Set `battery_estimate` to `false` to turn it all off. The add-on also runs
without estimates if the UPS settings cannot be read or look implausible; the
log says why.

## Using the NUT integration

The NUT server lets the standard Home Assistant **Network UPS Tools (NUT)**
integration, or any other NUT client, read the UPS.

1. Add the NUT integration and enter the Home Assistant host and port `3493`.
2. Leave **username and password empty**. The server is read-only and does not
   authenticate.
3. Choose the UPS offered by the integration.

From a shell on another machine you can check it with:

```bash
upsc openups@<home-assistant-host>:3493
```

The NUT variables `battery.charge` and `battery.runtime` carry the same estimates.

`SET VAR`, `INSTCMD` and `FSD` are not supported, so the UPS cannot be
configured or shut down through NUT. `ups.status` shows `OL` on mains, `OL CHRG`
while the battery is actually charging and `OB DISCHRG` on battery. If polling
fails repeatedly, clients get `DATA-STALE`.

## Troubleshooting

**`usb: device not found` and the add-on retries.** The UPS is not visible to the
container. Check that it is plugged into the Home Assistant machine and that
another add-on or service is not using it. If you have several USB devices, try
`device_index` or `device_path`.

**Permission or "claim interface" errors.** Another process holds the device,
most likely the NUT add-on. Stop it and restart this add-on.

**Port 3493 already in use.** The NUT add-on or another `upsd` is running. Stop
it, or change `nut_listen` (and the port in your NUT client).

**No entities appear.** Check that `discovery` is `true`, that `mqtt_prefix`
matches the prefix in the MQTT integration, and that the log shows
`mqtt: connected`. Broker credentials go in `mqtt_username` and `mqtt_password`.

**Values look wrong on an experimental model.** Expected: offsets are
unverified. The add-on image contains a `hidprobe` tool. Running
`hidprobe -pid 0xd005 -decode` (use your model's PID) from a shell in the
container prints the decoded values, and `hidprobe -pid 0xd005` prints the raw
frames. Attaching that output to an issue on
<https://github.com/herrfrei/openupsd> is the way to get a model verified.

## Support

Report problems and share captures at
<https://github.com/herrfrei/openupsd/issues>. Include the add-on log and your
UPS model.
