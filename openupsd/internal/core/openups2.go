package core

import (
	"fmt"

	"github.com/herrfrei/openupsd/internal/usb"
)

var OpenUPS2 = Device{
	Model:     "openups2",
	ModelName: "OpenUPS2",
	Desc:      "Mini-Box OpenUPS2",
	Mfr:       "Mini-Box.com",
	PID:       0xD005,
	Size:      32,
	Requests:  []byte{0x81, 0x83, 0x85, 0x87},
	Untested:  true,
	Sensors: append([]Sensor{
		{Slug: "vin", Name: "VIN", NUT: "input.voltage", Unit: "V", Class: "voltage", StateClass: "measurement", Icon: "mdi:flash", Precision: 2},
		{Slug: "vout", Name: "VOut", NUT: "output.voltage", Unit: "V", Class: "voltage", StateClass: "measurement", Icon: "mdi:flash", Precision: 2},
		{Slug: "vbat", Name: "VBat", NUT: "battery.voltage", Unit: "V", Class: "voltage", StateClass: "measurement", Icon: "mdi:battery", Precision: 2},
		{Slug: "c_charge", Name: "Charge Current", Unit: "A", Class: "current", StateClass: "measurement", Icon: "mdi:current-dc", Precision: 2},
		{Slug: "c_discharge", Name: "Discharge Current", Unit: "A", Class: "current", StateClass: "measurement", Icon: "mdi:current-dc", Precision: 2},
		{Slug: "temperature", Name: "Temperature", NUT: "ups.temperature", Unit: "°C", Class: "temperature", StateClass: "measurement", Icon: "mdi:thermometer", Precision: 0},
		{Slug: "temperature_2", Name: "Temperature 2", Unit: "°C", Class: "temperature", StateClass: "measurement", Icon: "mdi:thermometer", Category: "diagnostic", Precision: 0},
		{Slug: "temperature_3", Name: "Temperature 3", Unit: "°C", Class: "temperature", StateClass: "measurement", Icon: "mdi:thermometer", Category: "diagnostic", Precision: 0},
		{Slug: "duty", Name: "Output Duty", Unit: "", StateClass: "measurement", Icon: "mdi:sine-wave", Category: "diagnostic", Precision: 3},
		{Slug: "capacity", Name: "Battery Level", NUT: "battery.charge", Unit: "%", Class: "battery", StateClass: "measurement", Icon: "mdi:battery", Precision: 0},
		{Slug: "runtime", Name: "Runtime", NUT: "battery.runtime", Unit: "s", Class: "duration", StateClass: "measurement", Icon: "mdi:timer-outline", Precision: 0},
		{Slug: "power_state", Name: "Power State", Icon: "mdi:power-plug", Text: true},
		{Slug: "firmware", Name: "Firmware", Icon: "mdi:chip", Category: "diagnostic", Text: true},
	}, cellSensors(3, 3)...),
	Poll: pollOpenUPS2,
}

func pollOpenUPS2(d usb.Dev, size int) (Reading, error) {
	var r Reading
	buf := make([]byte, size)

	n, err := d.Exchange(0x81, nil, buf)
	if err != nil {
		return r, fmt.Errorf("0x81: %w", err)
	}
	m := buf[:n]
	if len(m) < size || m[0] != 0x82 {
		return r, fmt.Errorf("0x81: unexpected reply %#02x", replyType(m))
	}

	r.Firmware = fmt.Sprintf("%d.%d", m[31]>>4, m[31]&0x0F)
	vin := float64(le16(m, 1)) / 1000
	vbat := float64(le16(m, 5)) / 1000
	cchg := float64(le16(m, 19)) / 1000
	cdis := float64(le16(m, 21)) / 1000

	r.SetNum("vin", vin)
	r.SetNum("vout", float64(le16(m, 3))/1000)
	r.SetNum("vbat", vbat)
	r.SetNum("c_charge", cchg)
	r.SetNum("c_discharge", cdis)
	for i := 0; i < 3; i++ {
		r.SetNum(fmt.Sprintf("vcell_%d", i+1), float64(le16(m, 7+2*i))/1000)
	}
	// readMode 15 -> little-endian half-words
	for i := 0; i < 3; i++ {
		slug := "temperature"
		if i > 0 {
			slug = fmt.Sprintf("temperature_%d", i+1)
		}
		r.SetNum(slug, termistorToTemp(le16(m, 13+2*i)))
	}
	r.SetNum("duty", float64(le16(m, 26))/1024)

	switch {
	case m[24]&(1<<6) != 0:
		r.Status = "OB DISCHRG"
		r.SetStr("power_state", "battery")
	case m[24]&(1<<5) != 0:
		if cchg >= 0.05 {
			r.Status = "OL CHRG"
		} else {
			r.Status = "OL"
		}
		r.SetStr("power_state", "charging")
	default:
		r.Status = "OL"
		r.SetStr("power_state", "idle")
	}
	// battery.current is signed, positive on discharge.
	r.SetVar("battery.current", fmt.Sprintf("%.3f", cdis-cchg))

	// CLOCK_OUT works on the OpenUPS2, unlike on the OpenUPS.
	n, err = d.Exchange(0x83, nil, buf)
	if err != nil {
		return r, fmt.Errorf("0x83: %w", err)
	}
	c := buf[:n]
	if len(c) < size || c[0] != 0x84 {
		return r, fmt.Errorf("0x83: unexpected reply %#02x", replyType(c))
	}
	r.SetNum("capacity", float64(c[12]))
	r.SetNum("runtime", float64(le16(c, 16)))
	return r, nil
}
