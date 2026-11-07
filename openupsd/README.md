# openupsd

A standalone Go daemon for Mini-Box USB UPS devices. It replaces a running NUT
server: it talks to the device over USB, serves the NUT network protocol on
TCP 3493, and publishes the same values to MQTT with Home Assistant discovery.

Written to be portable. There is no CGO, no libusb and no hidapi. The binary is
statically linked and cross-compiles for amd64, arm64 and armv7.

## Status

| Model | PID | Report | Status |
|---|---|---|---|
| OpenUPS | `0xd004` | 32 bytes | **Verified** against real hardware (fw 1.9) |
| OpenUPS2 | `0xd005` | 32 bytes | Experimental, offsets unverified |
| NUC-UPS | `0xd007` | 32 bytes | Experimental, offsets unverified |
| DCDC-USB | `0xd003` | 24 bytes | Experimental, offsets unverified |

Only the OpenUPS is auto-detected. Experimental models must be selected
explicitly with `-model`, and the daemon logs a warning when you do. A wrong
byte offset produces plausible-looking but wrong numbers in Home Assistant and
NUT, which is worse than not detecting the device at all.

The NUT subset is read-only. `SET VAR`, `INSTCMD`, `FSD` and authentication
return `ERR ACCESS-DENIED` or are accepted without granting anything.

## Features

- **USB transport through usbfs** (`/dev/bus/usb/...`), the same endpoints
  pyusb uses. Kernel HID driver is detached, interface 0 is claimed, then the
  raw 32-byte reports are exchanged on endpoints `0x01`/`0x81`.
- **NUT server** on TCP 3493 implementing `HELP`, `VER`, `NETVER`, `GET
  UPSDESC|NUMLOGINS|VAR|TYPE|DESC`, `LIST UPS|VAR|RW|CMD|CLIENT|ENUM|RANGE`,
  `USERNAME`, `PASSWORD`, `LOGIN`, `LOGOUT`, `STARTTLS`. Responses use upsd's
  quoting rules and `DATA-STALE` when polling has failed.
- **MQTT** with Home Assistant discovery, retained configuration, availability
  via LWT and `expire_after` on status entities.
- **Reconnect** on USB errors (3 failures, then reopen) and on MQTT connection
  loss, with discovery republished after every reconnect.
- **Serial number** read from the USB string descriptor when the device
  provides one.

## Requirements

- Linux. The transport uses usbfs directly, so there is no Windows or macOS
  support.
- Read/write access to the device node. On Home Assistant OS the add-on runs as
  root; elsewhere either run as root or install a udev rule:

  ```
  SUBSYSTEM=="usb", ATTRS{idVendor}=="04d8", MODE="0660", GROUP="plugdev"
  ```

- The USB bus must be visible to the process. In Docker:

  ```bash
  docker run --device /dev/bus/usb ...
  ```

- Port 3493 free. Stop any existing `nut-server`/`upsd` and the NUT add-on
  first: it claims the same device interface and the same TCP port.

## Build

```bash
go build -o openupsd ./cmd/openupsd
go build -o hidprobe ./cmd/hidprobe
```

Static cross-compiled binaries:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o openupsd-amd64 ./cmd/openupsd
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o openupsd-arm64 ./cmd/openupsd
CGO_ENABLED=0 GOOS=linux GOARCH=arm   GOARM=7 go build -o openupsd-armv7 ./cmd/openupsd
```

`file openupsd-amd64` should report "statically linked".

## Run

```bash
# Detect the model, serve NUT on :3493, publish to MQTT
./openupsd -mqtt 192.168.1.10 -listen :3493

# Explicit device node (from lsusb: Bus 003 Device 009)
./openupsd -dev /dev/bus/usb/003/009

# NUT only, no MQTT
./openupsd -mqtt ""

# Experimental model, opt in explicitly
./openupsd -model openups2
```

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `-model` | `auto` | `openups`, `openups2`, `nucups`, `dcdcusb` or `auto` |
| `-dev` | | explicit usbfs node; the PID is read from the descriptor |
| `-n` | `0` | device index when several match |
| `-listen` | `:3493` | NUT listen address |
| `-name` | from model | NUT UPS name |
| `-interval` | `5s` | poll interval, minimum 1s |
| `-mqtt` | `core-mosquitto` | broker host; empty string disables MQTT |
| `-mqtt-port` | `1883` | broker port |
| `-mqtt-username`, `-mqtt-password` | | broker credentials |
| `-mqtt-tls` | `false` | TLS to the broker |
| `-mqtt-ca-certs` | | PEM bundle for a private CA |
| `-mqtt-prefix` | `homeassistant` | discovery prefix |
| `-node-id` | `openups` | used in topics and unique IDs |

Options are also read from `/data/options.json` (override the path with
`OPENUPSD_OPTIONS`); flags win over the file.

## Verifying the device

`hidprobe` dumps raw reports and, with `-decode`, runs the model parser:

```bash
./hidprobe -pid 0xd004 -decode
```

```
serial: ""
firmware: 1.9
status:   OL
vin                  V   13.26
vout                 V   12.06
vbat                 V   19.96
...
```

Without `-decode` it prints every request/response pair the model uses, which
is what a new or unverified model needs:

```bash
./hidprobe -pid 0xd004
TX 8100000000000000000000000000000000000000000000000000000000000000
RX 827601d5...  [32 bytes, type 0x82 ok]
```

A physically consistent capture is the cheapest sanity check: `c_in × vin`
should be close to `output_power`, and the six cell voltages should sum to
roughly `vbat`.

## NUT

```bash
printf 'LIST UPS\nLIST VAR openups\nLOGOUT\n' | nc localhost 3493
upsc openups@localhost:3493
```

Point the Home Assistant NUT integration at this host and port instead of the
old server. Because it is read-only, leave username and password empty.

Reported variables: `device.mfr`, `device.model`, `device.type`, `driver.name`,
`driver.version`, `ups.mfr`, `ups.model`, `ups.firmware`, `ups.status`,
`ups.temperature`, `ups.realpower`, `input.voltage`, `input.current`,
`output.voltage`, `battery.voltage`, `battery.current`, plus `device.serial`
and `ups.serial` when the device exposes a serial.

`battery.charge` and `battery.runtime` are deliberately absent on the OpenUPS:
the firmware only reports them in the `CLOCK_OUT` reply, which breaks
communication on that model. A guessed SOC would be worse than no value.

## MQTT

State topics: `<base>/sensor/<slug>/state`, retained.
Availability: `<base>/availability`, `online`/`offline` via LWT.
Discovery: `<prefix>/sensor/<node-id>/<slug>/config`, retained.

`<base>` defaults to `openups/<node-id>`. Status entities carry
`expire_after: 60`, so they become `unknown` within a minute of the daemon
dying. `suggested_display_precision` is set per sensor, which is what makes
Home Assistant show `13.26 V` instead of `13 V`.

## Home Assistant add-on

The add-on lives in this directory (`config.yaml`, `build.yaml`, `Dockerfile`,
`run.sh`) and is built locally by the Supervisor; no prebuilt image is
published. It needs the USB bus, not privileged mode:

```yaml
usb: true
```

Notes:

- Device numbers change after a replug or reboot. Prefer auto-detection; use
  `device_path` only if you must.
- The binary is static, so the image needs no CGO toolchain, no libusb and no
  hidapi. Keep it that way — a glibc-linked build fails on Alpine with
  `__mbstowcs_chk: symbol not found`.

## Development

```bash
gofmt -l . && go vet ./... && go test ./...
```

Layout:

```
cmd/openupsd/        daemon: USB polling, NUT server, MQTT bridge
cmd/hidprobe/        capture and decode tool
internal/usb/        usbfs transport, framing, timeouts
internal/core/       device registry, per-model decoders, status store
internal/nut/        NUT protocol server
internal/mqtt/       MQTT client and HA discovery
```

The experimental models have no tests built from real captures. Real captures
are required before removing the `Untested` flag from a device.

## Known limitations

- Read-only NUT server: no `SET VAR`, no `INSTCMD`.
- No `LB` (low battery) status; thresholds live in device flash.
- OpenUPS always reports six cells, even in a 4-cell configuration. Reading
  `CELLS` from flash would fix this and also unlock `battery.charge`.
- DCDC-USB is not a UPS in dumb/script mode; `ups.status` is only `OL` or
  `OFF`.
- No write path to device flash. Nothing in this daemon modifies the device.

## Acknowledgments

Protocol behaviour was derived from the Mini-Box C++ `ups` utility and from
pyopenups, then checked against real hardware. Home Assistant discovery follows
the MQTT integration documentation.

## License

MIT, see [LICENSE](../LICENSE).
