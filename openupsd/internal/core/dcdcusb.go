package core

import (
	"fmt"
	"math"
	"time"

	"github.com/herrfrei/openupsd/internal/usb"
)

var DCDCUSB = Device{
	Model:     "dcdcusb",
	ModelName: "DCDC-USB",
	Desc:      "Mini-Box DCDC-USB",
	Mfr:       "Mini-Box.com",
	PID:       0xD003,
	Size:      24,
	Requests:  []byte{0x81, 0xB1},
	Untested:  true,
	Sensors: []Sensor{
		{Slug: "vin", Name: "VIN", NUT: "input.voltage", Unit: "V", Class: "voltage", StateClass: "measurement", Icon: "mdi:flash", Precision: 2},
		{Slug: "vout", Name: "VOut", NUT: "output.voltage", Unit: "V", Class: "voltage", StateClass: "measurement", Icon: "mdi:flash", Precision: 2},
		{Slug: "vout_set", Name: "VOut Configured", Unit: "V", Class: "voltage", StateClass: "measurement", Icon: "mdi:tune", Category: "diagnostic", Precision: 2},
		{Slug: "v_ign", Name: "Ignition Voltage", Unit: "V", Class: "voltage", StateClass: "measurement", Icon: "mdi:car-key", Precision: 2},
		{Slug: "output_on", Name: "Output", Icon: "mdi:power", Text: true},
		{Slug: "aux_vin", Name: "Aux VIN", Icon: "mdi:electric-switch", Category: "diagnostic", Text: true},
		{Slug: "power_switch", Name: "Power Switch", Icon: "mdi:button-pointer", Category: "diagnostic", Text: true},
		{Slug: "mode", Name: "Mode", Icon: "mdi:cog", Category: "diagnostic", Text: true},
		{Slug: "timer_wait", Name: "Timer Wait", Icon: "mdi:timer-outline", Category: "diagnostic", Text: true},
		{Slug: "timer_off_delay", Name: "Timer Off Delay", Icon: "mdi:timer-outline", Category: "diagnostic", Text: true},
		{Slug: "timer_hard_off", Name: "Timer Hard Off", Icon: "mdi:timer-outline", Category: "diagnostic", Text: true},
		{Slug: "firmware", Name: "Firmware", Icon: "mdi:chip", Category: "diagnostic", Text: true},
	},
	Poll: pollDCDCUSB,
}

// dcdcVOut applies the DCDCUSB_CT_* constants from HIDDCDCUSB.h to a wiper
// register value.
func dcdcVOut(reg byte) float64 {
	rpot := float64(reg)*10000/257 + 75
	v := 80 * (1 + 49900/(rpot+1500))
	return math.Floor(v) / 100
}

func pollDCDCUSB(d usb.Dev, size int) (Reading, error) {
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

	mode := m[1] & 0x03
	status1 := m[6]
	r.SetNum("vin", float64(m[3])*0.1558)
	r.SetNum("v_ign", float64(m[4])*0.1558)
	r.SetNum("vout", float64(m[5])*0.1170)
	// DCDC-USB packs the minor version into 5 bits, unlike the 4/4 split.
	r.Firmware = fmt.Sprintf("%d.%d", (m[23]>>5)&0x07, m[23]&0x1F)
	r.SetStr("mode", dcdcMode(mode))
	r.SetStr("output_on", onOff(status1&0x08 != 0))
	r.SetStr("aux_vin", onOff(status1&0x10 != 0))
	r.SetStr("power_switch", onOff(status1&0x04 != 0))
	// The C++ code prints these from the config buffer by mistake; use the report.
	r.SetStr("timer_wait", hms(le16(m, 11)))
	r.SetStr("timer_off_delay", hms(le16(m, 19)))
	r.SetStr("timer_hard_off", hms(le16(m, 21)))
	// ups.temperature is not reported by this model, so it stays unset.

	if status1&0x08 != 0 {
		r.Status = "OL"
	} else {
		r.Status = "OFF"
	}

	n, err = d.Exchange(0xB1, []byte{0x07, 0x00, 0x00}, buf)
	if err != nil {
		return r, fmt.Errorf("0xB1: %w", err)
	}
	c := buf[:n]
	if len(c) < 4 || c[0] != 0xB2 {
		return r, fmt.Errorf("0xB1: unexpected reply %#02x", replyType(c))
	}
	if c[1] == 0 {
		r.SetNum("vout_set", dcdcVOut(c[3]))
	}
	return r, nil
}

func dcdcMode(m byte) string {
	switch m {
	case 0:
		return "dumb"
	case 1:
		return "automotive"
	case 2:
		return "script"
	default:
		return "ups"
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func hms(sec uint16) string {
	if sec == 0xFFFF {
		return "never"
	}
	d := time.Duration(sec) * time.Second
	return fmt.Sprintf("%02d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
}
