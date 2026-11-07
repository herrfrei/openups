# OpenUPSd: Mini-Box UPS for Home Assistant

[![CI](https://github.com/herrfrei/openups/actions/workflows/build.yml/badge.svg)](https://github.com/herrfrei/openups/actions/workflows/build.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

[![Add this repository to your Home Assistant instance.](https://my.home-assistant.io/badges/supervisor_add_addon_repository.svg)](https://my.home-assistant.io/redirect/supervisor_add_addon_repository/?repository_url=https%3A%2F%2Fgithub.com%2Fherrfrei%2Fopenups)

OpenUPSd reads a Mini-Box USB UPS directly over USB and makes it available to
Home Assistant in two ways at once:

- **MQTT** with Home Assistant discovery: voltages, current, power, temperature
  and power state show up as entities without any manual YAML.
- **NUT server** on TCP 3493: the standard Home Assistant NUT integration (or
  `upsc`, or any other NUT client) can talk to it as if it were `upsd`.

It is a single static Go binary. There is no CGO, no libusb and no hidapi, so it
runs on Home Assistant OS (Alpine/musl) and on any other Linux host.

## Supported devices

| Model | USB ID | Status |
|---|---|---|
| OpenUPS | `04d8:d004` | **Verified** on real hardware (firmware 1.9) |
| OpenUPS2 | `04d8:d005` | Experimental, byte offsets unverified |
| NUC-UPS | `04d8:d007` | Experimental, byte offsets unverified |
| DCDC-USB | `04d8:d003` | Experimental, byte offsets unverified |

Only the OpenUPS is detected automatically. The experimental models must be
selected explicitly via the `model` option and log a warning when you do. A wrong
offset produces plausible but wrong numbers, so they are never auto-detected. If
you own one of these, a capture from `hidprobe` (see below) is the most useful
contribution you can make.

## Installation (Home Assistant add-on)

1. Click the button above, or in Home Assistant go to **Settings → Add-ons →
   Add-on Store → ⋮ → Repositories** and add
   `https://github.com/herrfrei/openups`.
2. Install **OpenUPSd**. The add-on is compiled on your Home Assistant machine,
   so the first install downloads a Go toolchain image and can take a few
   minutes, especially on a Raspberry Pi.
3. **Stop any other NUT server first.** The existing NUT add-on or `upsd` claims
   the same USB interface and the same TCP port 3493.
4. Check the options (the defaults work with the Mosquitto add-on) and start the
   add-on. Check the log: it prints the detected model and firmware.

### Options

| Option | Default | Meaning |
|---|---|---|
| `model` | `auto` | `openups`, `openups2`, `nucups`, `dcdcusb` or `auto` |
| `ups_name` | empty | NUT UPS name; empty uses the model default |
| `mqtt_host` | `core-mosquitto` | broker host |
| `mqtt_port` | `1883` | broker port |
| `mqtt_username`, `mqtt_password` | empty | broker credentials |
| `mqtt_prefix` | `homeassistant` | MQTT discovery prefix |
| `node_id` | `openups` | used in topics and entity unique IDs |
| `nut_listen` | `:3493` | NUT listen address |
| `interval` | `5` | poll interval in seconds |
| `discovery` | `true` | publish Home Assistant discovery messages |
| `device_path` | empty | explicit `/dev/bus/usb/BBB/DDD` node; prefer auto-detection |
| `device_index` | `0` | which device to use if several match |

`node_id` becomes part of every entity's `unique_id`. Changing it later creates
new entities and orphans the old ones.

### Using the NUT integration

Point the Home Assistant NUT integration at the add-on host and port 3493 and
leave username and password empty. The NUT server is read-only.

## Standalone use

The same binary runs outside Home Assistant:

```bash
cd openupsd
CGO_ENABLED=0 go build -o openupsd ./cmd/openupsd
./openupsd -mqtt 192.168.1.10 -listen :3493
upsc openups@localhost:3493
```

Flags, requirements (device permissions, udev rule, Docker `--device`), the full
variable list and the MQTT topic layout are documented in
[`openupsd/README.md`](openupsd/README.md).

## Checking a device with `hidprobe`

`hidprobe` is built alongside the daemon (inside the add-on image it lives at
`/usr/local/bin/hidprobe`). It decodes a live report or dumps the raw frames:

```bash
hidprobe -pid 0xd004 -decode   # decoded values
hidprobe -pid 0xd004           # raw TX/RX frames, for new models
```

## Limitations

- The NUT server is read-only; there is no `SET VAR` or `INSTCMD`.
- `battery.charge`, `battery.runtime` and the low-battery (`LB`) status are not
  reported yet. The OpenUPS only provides them in a reply that breaks
  communication, and a guessed value would be worse than none.
- The OpenUPS always reports six cells, even in a 4-cell configuration.
- Nothing in this project writes to the device's flash.
- Linux only: the USB transport uses usbfs.

## Repository layout

```
repository.yaml     Home Assistant repository metadata
openupsd/           the add-on and the Go module
  config.yaml       add-on definition
  Dockerfile        built locally by the Supervisor
  cmd/              openupsd daemon, hidprobe tool
  internal/         usb, core (device decoders), nut, mqtt
```

## Development

```bash
cd openupsd
gofmt -l . && go vet ./... && go test ./...
```

CI runs the same checks plus a build of the add-on Dockerfile. No images are
published.

## Acknowledgments

Protocol behaviour was derived from the Mini-Box C++ `ups` utility and from
pyopenups, then checked against real hardware.

## License

[MIT](LICENSE)
