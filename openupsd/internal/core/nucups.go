package core

import (
	"fmt"

	"github.com/herrfrei/openupsd/internal/usb"
)

var NUCUPS = Device{
	Model:     "nucups",
	ModelName: "NUC-UPS",
	Desc:      "Mini-Box NUC-UPS",
	Mfr:       "Mini-Box.com",
	PID:       0xD007,
	Size:      32,
	Requests:  []byte{0x81, 0x83, 0x85, 0x87},
	Untested:  true,
	Sensors: append([]Sensor{
		{Slug: "vin", Name: "VIN", NUT: "input.voltage", Unit: "V", Class: "voltage", StateClass: "measurement", Icon: "mdi:flash", Precision: 2},
		{Slug: "vout", Name: "VOut", NUT: "output.voltage", Unit: "V", Class: "voltage", StateClass: "measurement", Icon: "mdi:flash", Precision: 2},
		{Slug: "iout", Name: "Output Current", NUT: "output.current", Unit: "A", Class: "current", StateClass: "measurement", Icon: "mdi:current-dc", Precision: 3},
		{Slug: "pout", Name: "Output Power", NUT: "ups.realpower", Unit: "W", Class: "power", StateClass: "measurement", Icon: "mdi:flash", Precision: 3},
		{Slug: "v_ign", Name: "Ignition Voltage", Unit: "V", Class: "voltage", StateClass: "measurement", Icon: "mdi:car-key", Precision: 2},
		{Slug: "temperature_1", Name: "Cell 1 Temperature", Unit: "°C", Class: "temperature", StateClass: "measurement", Icon: "mdi:thermometer", Precision: 0},
		{Slug: "temperature_2", Name: "Cell 2 Temperature", Unit: "°C", Class: "temperature", StateClass: "measurement", Icon: "mdi:thermometer", Precision: 0},
		{Slug: "temperature_3", Name: "Cell 3 Temperature", Unit: "°C", Class: "temperature", StateClass: "measurement", Icon: "mdi:thermometer", Precision: 0},
		{Slug: "temperature_4", Name: "Cell 4 Temperature", Unit: "°C", Class: "temperature", StateClass: "measurement", Icon: "mdi:thermometer", Precision: 0},
		{Slug: "vbat", Name: "Battery Voltage", NUT: "battery.voltage", Unit: "V", Class: "voltage", StateClass: "measurement", Icon: "mdi:battery", Precision: 2},
		{Slug: "batpack", Name: "Battery Pack Voltage", Unit: "V", Class: "voltage", StateClass: "measurement", Icon: "mdi:battery", Category: "diagnostic", Precision: 2},
		{Slug: "i_chg_dchg", Name: "Charge/Discharge Current", NUT: "battery.current", Unit: "A", Class: "current", StateClass: "measurement", Icon: "mdi:current-dc", Precision: 3},
		{Slug: "battery_charge", Name: "Battery Level", NUT: "battery.charge", Unit: "%", Class: "battery", StateClass: "measurement", Icon: "mdi:battery", Precision: 0},
		{Slug: "mode", Name: "Mode", Icon: "mdi:cog", Category: "diagnostic", Text: true},
		{Slug: "firmware", Name: "Firmware", Icon: "mdi:chip", Category: "diagnostic", Text: true},
	}, cellSensors(4, 3)...),
	Poll: pollNUCUPS,
}

func pollNUCUPS(d usb.Dev, size int) (Reading, error) {
	var r Reading
	buf := make([]byte, size)

	ex := func(req, want byte) ([]byte, error) {
		n, err := d.Exchange(req, nil, buf)
		if err != nil {
			return nil, fmt.Errorf("%#02x: %w", req, err)
		}
		if n < size || buf[0] != want {
			return nil, fmt.Errorf("%#02x: unexpected reply %#02x", req, replyType(buf[:n]))
		}
		return buf[:n], nil
	}

	// 0x82: IO data. All 16/32-bit values are big-endian on this model.
	a, err := ex(0x81, 0x82)
	if err != nil {
		return r, err
	}
	stateFlags := a[5]
	vin := float64(be16(a, 6)) / 1000
	iout := float64(be16(a, 8)) / 1000
	vout := float64(be16(a, 10)) / 1000
	vign := float64(be16(a, 12)) / 1000
	pout := float64(be32(a, 14)) / 1000
	var temps [4]float64
	for i := 0; i < 4; i++ {
		temps[i] = termistorToTemp(be16(a, 18+2*i))
	}
	ichg := float64(int16(be16(a, 26))) / 1000

	r.SetNum("vin", vin)
	r.SetNum("vout", vout)
	r.SetNum("iout", iout)
	r.SetNum("pout", pout)
	r.SetNum("v_ign", vign)
	for i := 0; i < 4; i++ {
		r.SetNum(fmt.Sprintf("temperature_%d", i+1), temps[i])
	}
	r.SetNum("i_chg_dchg", ichg)
	// ups.temperature has no matching sensor on this model; set it directly.
	r.SetVar("ups.temperature", fmt.Sprintf("%.0f", temps[0]))

	// 0x84: timers, state machines, firmware.
	b, err := ex(0x83, 0x84)
	if err != nil {
		return r, err
	}
	r.Firmware = fmt.Sprintf("%d.%d", b[25]>>4, b[25]&0x0F)
	r.SetStr("mode", nucMode(b[22]))

	// 0x86: cell voltages, pack voltages.
	c, err := ex(0x85, 0x86)
	if err != nil {
		return r, err
	}
	for i := 0; i < 4; i++ {
		r.SetNum(fmt.Sprintf("vcell_%d", i+1), float64(be16(c, 20+2*i))/1000)
	}
	r.SetNum("vbat", float64(be16(c, 28))/1000)
	r.SetNum("batpack", float64(be16(c, 30))/1000)

	// 0x88: charger flags, battery level.
	e, err := ex(0x87, 0x88)
	if err != nil {
		return r, err
	}
	level := float64(e[12])
	chargerFlags := uint32(e[13])<<16 | uint32(e[1])<<8 | uint32(e[2])
	r.SetNum("battery_charge", level)

	switch {
	case stateFlags&0x01 == 0: // VIN_GOOD clear
		r.Status = "OB DISCHRG"
	case chargerFlags&(1<<6) != 0 && chargerFlags&(1<<10) == 0: // charging, not full
		r.Status = "OL CHRG"
	default:
		r.Status = "OL"
	}
	if chargerFlags&(1<<7) != 0 && chargerFlags&(1<<10) != 0 {
		r.SetVar("battery.charger.status", "resting")
	}
	return r, nil
}

func nucMode(m byte) string {
	switch m {
	case 0:
		return "dumb"
	case 1:
		return "automotive"
	default:
		return fmt.Sprintf("mode %d", m)
	}
}
