package core

import (
	"fmt"

	"github.com/herrfrei/openupsd/internal/usb"
)

var OpenUPS = Device{
	Model:     "openups",
	ModelName: "OpenUPS",
	Desc:      "Mini-Box OpenUPS",
	Mfr:       "Mini-Box.com",
	PID:       0xD004,
	Size:      32,
	Requests:  []byte{0x81, 0x85},
	Sensors:   append(openUPSSensors(), cellSensors(6, 3)...),
	Poll:      pollOpenUPS,
}

func openUPSSensors() []Sensor {
	return []Sensor{
		{Slug: "vin", Name: "VIN", NUT: "input.voltage", Unit: "V", Class: "voltage", StateClass: "measurement", Icon: "mdi:flash", Precision: 2},
		{Slug: "vout", Name: "VOut", NUT: "output.voltage", Unit: "V", Class: "voltage", StateClass: "measurement", Icon: "mdi:flash", Precision: 2},
		{Slug: "vbat", Name: "VBat", NUT: "battery.voltage", Unit: "V", Class: "voltage", StateClass: "measurement", Icon: "mdi:battery", Precision: 2},
		{Slug: "c_charge", Name: "Charge Current", Unit: "A", Class: "current", StateClass: "measurement", Icon: "mdi:current-dc", Precision: 2},
		{Slug: "c_discharge", Name: "Discharge Current", Unit: "A", Class: "current", StateClass: "measurement", Icon: "mdi:current-dc", Precision: 2},
		{Slug: "c_in", Name: "Input Current", NUT: "input.current", Unit: "A", Class: "current", StateClass: "measurement", Icon: "mdi:current-ac", Precision: 2},
		{Slug: "temperature", Name: "Temperature", NUT: "ups.temperature", Unit: "°C", Class: "temperature", StateClass: "measurement", Icon: "mdi:thermometer", Precision: 1},
		{Slug: "output_power", Name: "Output Power", NUT: "ups.realpower", Unit: "W", Class: "power", StateClass: "measurement", Icon: "mdi:flash", Precision: 1},
		{Slug: "power_state", Name: "Power State", Icon: "mdi:power-plug", Text: true},
		{Slug: "firmware", Name: "Firmware", Icon: "mdi:chip", Category: "diagnostic", Text: true},
	}
}

func pollOpenUPS(d usb.Dev, size int) (Reading, error) {
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
	vin := float64(le16(m, 1)) * 0.03545
	vout := float64(le16(m, 3)) * 0.02571
	vbat := float64(le16(m, 5)) * 0.00857
	cchg := float64(le16(m, 19)) * 0.0008274
	cur := float64(le16(m, 21)) * 0.016113

	r.SetNum("vin", vin)
	r.SetNum("vout", vout)
	r.SetNum("vbat", vbat)
	r.SetNum("c_charge", cchg)
	for i := 0; i < 6; i++ {
		r.SetNum(fmt.Sprintf("vcell_%d", i+1), float64(le16(m, 7+2*i))*0.00857)
	}
	r.SetNum("temperature", termistorToTemp(le16(m, 26)))

	var cin, cdis float64
	switch {
	case m[24]&(1<<6) != 0: // running on battery
		cdis = cur
		r.Status = "OB DISCHRG"
		r.SetStr("power_state", "battery")
	case m[24]&(1<<5) != 0: // charger active
		cin = cur
		if vin > 0 {
			cin += cchg * vbat / (vin * 0.90)
		}
		if cchg >= 0.05 {
			r.Status = "OL CHRG"
		} else {
			r.Status = "OL"
		}
		r.SetStr("power_state", "charging")
	default: // USB only
		r.Status = "OL"
		r.SetStr("power_state", "idle")
	}
	r.SetNum("c_discharge", cdis)
	r.SetNum("c_in", cin)
	// NUT: battery.current is signed, positive on discharge.
	r.SetVar("battery.current", fmt.Sprintf("%.2f", cdis-cchg))

	n, err = d.Exchange(0x85, nil, buf)
	if err != nil {
		return r, fmt.Errorf("0x85: %w", err)
	}
	p := buf[:n]
	if len(p) < size || p[0] != 0x86 {
		return r, fmt.Errorf("0x85: unexpected reply %#02x", replyType(p))
	}
	r.SetNum("output_power", float64(le32(p, 1))*0.000001)
	return r, nil
}
