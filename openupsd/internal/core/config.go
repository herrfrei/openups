package core

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Param is one OpenUPS configuration variable stored in device flash.
//
// The table (params_openups.go) is generated from pyopenups, which in turn is
// a port of the Mini-Box C++ tool. Offsets, lengths and read modes are NOT
// confirmed against this project's own hardware captures yet: dump the flash
// with `hidprobe -config` and compare before trusting any value.
type Param struct {
	Name  string
	Index int     // byte offset inside the 1 KiB settings area
	Len   int     // 1, 2 or 4 bytes, little endian
	Mode  int     // read mode from the C++ tool, selects the codec
	Mult  float64 // scale, or an index into memConstants for mode 10
	Unit  string
	Desc  string
}

// ParamByName finds a parameter, ignoring case.
func ParamByName(name string) (Param, bool) {
	for _, p := range OpenUPSParams {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return Param{}, false
}

// memConstants are the per-LSB scales selected by read mode 10.
var memConstants = [...]float64{
	0.03545, // 0 Vin
	0.02571, // 1 Vout
	0.00857, // 2 Vbat per cell
	12.411,  // 3 I charge hi
	0.8274,  // 4 I charge lo
	16.113,  // 5 I discharge
}

// Input-undervoltage selector table used by read mode 11 (UPS_VIN_MIN_STOP).
// Each entry is (CVR_1, CVR_2, volts); the stored bytes are [CVR_2, CVR_1].
type uvp struct {
	cvr1, cvr2 byte
	volts      float64
}

var uvpTable = [32]uvp{
	{0, 0, 9.08}, {0, 1, 10.21}, {0, 2, 11.34}, {0, 3, 12.48},
	{0, 4, 13.61}, {0, 5, 14.75}, {0, 6, 15.88}, {0, 7, 17.02},
	{0, 8, 18.15}, {0, 9, 19.28}, {0, 10, 20.42}, {0, 11, 21.55},
	{0, 12, 22.69}, {0, 13, 23.82}, {0, 14, 24.96}, {0, 15, 26.09},
	{1, 0, 0.0}, {1, 1, 1.51}, {1, 2, 3.03}, {1, 3, 4.54},
	{1, 4, 6.05}, {1, 5, 7.56}, {1, 6, 9.08}, {1, 7, 10.59},
	{1, 8, 12.1}, {1, 9, 13.61}, {1, 10, 15.13}, {1, 11, 16.64},
	{1, 12, 18.15}, {1, 13, 19.66}, {1, 14, 21.18}, {1, 15, 22.69},
}

// UVPVolts returns the voltage a mode-11 selector index stands for.
func UVPVolts(index int) (float64, bool) {
	if index < 0 || index >= len(uvpTable) {
		return 0, false
	}
	return uvpTable[index].volts, true
}

// Output-voltage digital potentiometer (read mode 7).
const (
	ctRW     = 75.0
	ctRP     = 5000.0
	ctR1Vout = 27000.0
	ctR2Vout = 750.0
)

func voltsFromVoutPot(b byte) float64 {
	rpot := float64(b)*ctRP/257.0 + ctRW
	v := 80.0 * (1.0 + ctR1Vout/(rpot+ctR2Vout))
	return math.Floor(v) / 100.0
}

func (p Param) rawSpan(flash []byte) ([]byte, error) {
	if p.Len != 1 && p.Len != 2 && p.Len != 4 {
		return nil, fmt.Errorf("%s: unsupported length %d", p.Name, p.Len)
	}
	if p.Index < 0 || p.Index+p.Len > len(flash) {
		return nil, fmt.Errorf("%s: offset %d+%d outside the %d byte image", p.Name, p.Index, p.Len, len(flash))
	}
	return flash[p.Index : p.Index+p.Len], nil
}

func leUint(b []byte) uint64 {
	var v uint64
	for i := len(b) - 1; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

// Decode renders the stored value as text. The formats intentionally match
// pyopenups byte for byte (%f for scaled values, truncation for plain
// integers), which is what the golden tests compare against.
func (p Param) Decode(flash []byte) (string, error) {
	b, err := p.rawSpan(flash)
	if err != nil {
		return "", err
	}
	raw := float64(leUint(b))
	value := raw * p.Mult

	switch p.Mode {
	case 10:
		c := int(p.Mult)
		if c < 0 || c >= len(memConstants) {
			return "", fmt.Errorf("%s: bad constant index %d", p.Name, c)
		}
		return fmt.Sprintf("%f", raw*memConstants[c]), nil
	case 11:
		if p.Len < 2 {
			return "", fmt.Errorf("%s: mode 11 needs 2 bytes", p.Name)
		}
		pos := -1
		for i, u := range uvpTable {
			if u.cvr1 == b[1] && u.cvr2 == b[0] {
				pos = i // last match wins, like the C++ loop
			}
		}
		return strconv.Itoa(pos), nil
	case 12, 13:
		return strconv.Itoa(int(b[0])), nil
	case 14:
		return fmt.Sprintf("%08b", b[0]), nil
	case 15:
		if p.Len < 2 {
			return "", fmt.Errorf("%s: mode 15 needs 2 bytes", p.Name)
		}
		return strconv.Itoa(int(termistorToTemp(le16(b, 0)))), nil
	case 16:
		if p.Mult == 1 {
			return fmt.Sprintf("%.0f", value), nil
		}
		return fmt.Sprintf("%.2f", value), nil
	case 3:
		iv := uint32(int64(value))
		if iv == 0xFFFF {
			return "Never", nil
		}
		return fmt.Sprintf("%02d:%02d:%02d", iv/3600, (iv/60)%60, iv%60), nil
	case 7:
		return fmt.Sprintf("%f", voltsFromVoutPot(b[0])), nil
	default: // 1, 17
		return strconv.FormatInt(int64(value), 10), nil
	}
}
