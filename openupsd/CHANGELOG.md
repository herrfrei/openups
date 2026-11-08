# Changelog

## 0.4.0

- Battery charge and runtime are now estimated for the OpenUPS and shown as
  Home Assistant entities (Battery Charge, Runtime Estimate, Charge Estimate)
  and as NUT `battery.charge` / `battery.runtime`. They are estimates, derived
  from the battery settings stored in the UPS and the measured currents. See
  the Documentation tab for how they work and where they can be wrong.
- The add-on reads the UPS settings (capacity, cell count, cutoff voltage,
  voltage curve) at start-up. It never writes to the UPS.
- New option `battery_estimate` (default on) to switch the estimates off.
- `hidprobe -config` dumps the UPS settings (read-only).
- Fixed: the add-on is now built by Home Assistant itself instead of pulling a
  prebuilt image.

## 0.3.1

- The add-on is compiled locally by the Supervisor; no prebuilt image is
  published any more.
- Removed the unneeded `SYS_ADMIN` privilege.

## 0.3.0

- First version: reads a Mini-Box OpenUPS directly over USB and publishes to
  MQTT with Home Assistant discovery and to a read-only NUT server on port 3493.
