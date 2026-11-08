package core

import (
	"fmt"
	"strconv"

	"github.com/herrfrei/openupsd/internal/usb"
)

const DriverVersion = "0.4.0"

// Sensor is one MQTT entity. NUT is the NUT variable name, "" = MQTT only.
type Sensor struct {
	Slug       string
	Name       string
	NUT        string
	Unit       string
	Class      string
	StateClass string
	Icon       string
	Category   string
	Precision  int
	Text       bool
}

// Reading is a device-independent snapshot produced by a Poller.
type Reading struct {
	Firmware string
	Status   string // NUT ups.status
	Num      map[string]float64
	Str      map[string]string
	Vars     map[string]string // extra NUT variables
}

func (r *Reading) SetNum(slug string, v float64) {
	if r.Num == nil {
		r.Num = make(map[string]float64)
	}
	r.Num[slug] = v
}

func (r *Reading) SetStr(slug, v string) {
	if r.Str == nil {
		r.Str = make(map[string]string)
	}
	r.Str[slug] = v
}

func (r *Reading) SetVar(k, v string) {
	if r.Vars == nil {
		r.Vars = make(map[string]string)
	}
	r.Vars[k] = v
}

// Value renders one sensor. The "firmware" slug is special: it lives in
// Reading.Firmware rather than in the Str map, because polling sets it before
// any per-model strings are known.
func (r Reading) Value(s Sensor) string {
	if s.Text {
		if s.Slug == "firmware" {
			return r.Firmware
		}
		return r.Str[s.Slug]
	}
	v, ok := r.Num[s.Slug]
	if !ok {
		return ""
	}
	return strconv.FormatFloat(v, 'f', s.Precision, 64)
}

type Device struct {
	Model     string // "openups2", also used as the default NUT ups name
	ModelName string // "OpenUPS2"
	Desc      string // "Mini-Box OpenUPS2"
	Mfr       string
	PID       uint16
	Size      int    // report size in bytes
	Requests  []byte // request types, used by hidprobe for raw captures
	Sensors   []Sensor
	Poll      func(d usb.Dev, size int) (Reading, error)

	// Untested marks models whose byte offsets have not been confirmed against
	// real hardware. They are never chosen by auto-detection, because a wrong
	// offset produces plausible-looking but wrong values in HA and NUT.
	Untested bool
}

var Registry = []*Device{&OpenUPS, &OpenUPS2, &NUCUPS, &DCDCUSB}

func DeviceByPID(pid uint16) (*Device, bool) {
	for _, d := range Registry {
		if d.PID == pid {
			return d, true
		}
	}
	return nil, false
}

func DeviceByName(name string) (*Device, bool) {
	for _, d := range Registry {
		if d.Model == name {
			return d, true
		}
	}
	return nil, false
}

// NUTVars flattens a reading into the NUT variable set.
func (d *Device) NUTVars(r Reading, serial string) map[string]string {
	v := map[string]string{
		"device.mfr":     d.Mfr,
		"device.model":   d.ModelName,
		"device.type":    "ups",
		"driver.name":    "openupsd",
		"driver.version": DriverVersion,
		"ups.mfr":        d.Mfr,
		"ups.model":      d.ModelName,
		"ups.status":     r.Status,
	}
	if r.Firmware != "" {
		v["ups.firmware"] = r.Firmware
	}
	if serial != "" {
		v["device.serial"] = serial
		v["ups.serial"] = serial
	}
	for _, s := range d.Sensors {
		if s.NUT == "" {
			continue
		}
		if val := r.Value(s); val != "" {
			v[s.NUT] = val
		}
	}
	for k, val := range r.Vars {
		v[k] = val
	}
	return v
}

// byte-order helpers
func le16(b []byte, i int) uint16 { return uint16(b[i]) | uint16(b[i+1])<<8 }
func be16(b []byte, i int) uint16 { return uint16(b[i])<<8 | uint16(b[i+1]) }
func le32(b []byte, i int) uint32 { return uint32(le16(b, i)) | uint32(le16(b, i+2))<<16 }
func be32(b []byte, i int) uint32 { return uint32(be16(b, i))<<16 | uint32(be16(b, i+2)) }

func replyType(m []byte) byte {
	if len(m) == 0 {
		return 0
	}
	return m[0]
}

// termTable is shared by OpenUPS and OpenUPS2 (identical tables in the C++ code).
var termTable = [34]uint16{
	0x31, 0x40, 0x53, 0x68, 0x82, 0xA0, 0xC3, 0xE9, 0x113, 0x13F, 0x16E, 0x19F,
	0x1CF, 0x200, 0x22F, 0x25C, 0x286, 0x2AE, 0x2D3, 0x2F4, 0x312, 0x32D, 0x345,
	0x35A, 0x36D, 0x37E, 0x38C, 0x399, 0x3A5, 0x3AF, 0x3B7, 0x3BF, 0x3C6, 0x3CC,
}

func termistorToTemp(t uint16) float64 {
	last := len(termTable) - 1
	if t <= termTable[0] {
		return -40
	}
	if t >= termTable[last] {
		return 125
	}
	pos := 0
	for i := last; i >= 0; i-- {
		if t >= termTable[i] {
			pos = i
			break
		}
	}
	if t == termTable[pos] {
		return float64(pos*5 - 40)
	}
	t1 := float64(pos*5 - 40)
	t2 := float64((pos+1)*5 - 40)
	d1, d2 := float64(termTable[pos]), float64(termTable[pos+1])
	return ceil((float64(t)-d1)*(t2-t1)/(d2-d1)) + t1
}

// ceil mirrors the C++ integer truncation-towards-zero-then-add behaviour.
func ceil(v float64) float64 {
	i := float64(int64(v))
	if v > i {
		return i + 1
	}
	return i
}

// cellSensors builds the common per-cell sensor block.
func cellSensors(n int, prec int) []Sensor {
	out := make([]Sensor, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Sensor{
			Slug:       fmt.Sprintf("vcell_%d", i+1),
			Name:       fmt.Sprintf("Cell %d Voltage", i+1),
			Unit:       "V",
			Class:      "voltage",
			StateClass: "measurement",
			Icon:       "mdi:battery-plus-variant",
			Precision:  prec,
		})
	}
	return out
}
