package core

import (
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"testing"
)

// fakeDev replays canned reports and records the request order.
type fakeDev struct {
	replies map[byte][]byte
	serial  string
	calls   []byte
}

func (f *fakeDev) Exchange(cType byte, data, reply []byte) (int, error) {
	f.calls = append(f.calls, cType)
	r, ok := f.replies[cType]
	if !ok {
		return 0, errors.New("fakeDev: no canned reply for this request")
	}
	if len(r) > len(reply) {
		return 0, errors.New("fakeDev: reply larger than buffer")
	}
	return copy(reply, r), nil
}

func (f *fakeDev) Serial() string { return f.serial }
func (f *fakeDev) Close() error   { return nil }

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// synth builds an all-zero report and lets the caller patch bytes into it.
func synth(size int, patch func([]byte)) []byte {
	b := make([]byte, size)
	patch(b)
	return b
}

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v (±%v)", name, got, want, tol)
	}
}

func wantNum(t *testing.T, r Reading, slug string, want, tol float64) {
	t.Helper()
	got, ok := r.Num[slug]
	if !ok {
		t.Errorf("%s missing", slug)
		return
	}
	near(t, slug, got, want, tol)
}

func wantStr(t *testing.T, r Reading, slug, want string) {
	t.Helper()
	if got := r.Str[slug]; got != want {
		t.Errorf("%s = %q, want %q", slug, got, want)
	}
}

// Captured from a real OpenUPS (fw 1.9) with hidprobe.
const (
	capture0x82 = "827601d50119098201840184018401830185010000a00000a0117602390c0719"
	capture0x86 = "8646750102800023000000000000000000000000000000000000000000000000"
)

func captureDev(t *testing.T) *fakeDev {
	t.Helper()
	return &fakeDev{
		serial: "SN1",
		replies: map[byte][]byte{
			0x81: mustHex(t, capture0x82),
			0x85: mustHex(t, capture0x86),
		},
	}
}

func TestOpenUPSCapture(t *testing.T) {
	d := captureDev(t)
	r, err := OpenUPS.Poll(d, OpenUPS.Size)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 2 || d.calls[0] != 0x81 || d.calls[1] != 0x85 {
		t.Errorf("request order %x, want 81 85", d.calls)
	}
	if r.Firmware != "1.9" {
		t.Errorf("firmware %q", r.Firmware)
	}
	if r.Status != "OL" { // charge current is 0, so no CHRG
		t.Errorf("status %q, want OL", r.Status)
	}
	wantStr(t, r, "power_state", "charging")

	wantNum(t, r, "vin", 13.2583, 1e-3)
	wantNum(t, r, "vout", 12.0580, 1e-3)
	wantNum(t, r, "vbat", 19.9595, 1e-3)
	wantNum(t, r, "vcell_1", 3.30802, 1e-3)
	wantNum(t, r, "vcell_6", 3.33373, 1e-3)
	wantNum(t, r, "c_charge", 0, 1e-9)
	wantNum(t, r, "c_discharge", 0, 1e-9)
	wantNum(t, r, "c_in", 2.57808, 1e-3) // charge term is 0, buck-boost current only
	wantNum(t, r, "temperature", 39, 1e-9)
	wantNum(t, r, "output_power", 33.64999, 1e-6)

	if len(r.Vars) == 0 {
		t.Fatal("no NUT overrides recorded")
	}
}

func TestOpenUPSNUTVars(t *testing.T) {
	r, err := OpenUPS.Poll(captureDev(t), OpenUPS.Size)
	if err != nil {
		t.Fatal(err)
	}
	v := OpenUPS.NUTVars(r, "SN1")

	want := map[string]string{
		"device.mfr":      "Mini-Box.com",
		"device.model":    "OpenUPS",
		"device.type":     "ups",
		"device.serial":   "SN1",
		"driver.name":     "openupsd",
		"driver.version":  DriverVersion,
		"ups.mfr":         "Mini-Box.com",
		"ups.model":       "OpenUPS",
		"ups.serial":      "SN1",
		"ups.firmware":    "1.9",
		"ups.status":      "OL",
		"ups.temperature": "39.0",
		"ups.realpower":   "33.6", // 33.64999 rounds down
		"input.voltage":   "13.26",
		"input.current":   "2.58",
		"output.voltage":  "12.06",
		"battery.voltage": "19.96",
		"battery.current": "0.00", // discharge-positive: cdis - cchg
	}
	for k, w := range want {
		if got := v[k]; got != w {
			t.Errorf("%s = %q, want %q", k, got, w)
		}
	}
	if _, ok := v["battery.charge"]; ok {
		t.Error("battery.charge must not be invented")
	}
}

func TestOpenUPSNoSerial(t *testing.T) {
	r, err := OpenUPS.Poll(captureDev(t), OpenUPS.Size)
	if err != nil {
		t.Fatal(err)
	}
	v := OpenUPS.NUTVars(r, "")
	for _, k := range []string{"device.serial", "ups.serial"} {
		if _, ok := v[k]; ok {
			t.Errorf("%s must be absent without a serial", k)
		}
	}
}

func TestOpenUPSCells(tt *testing.T) {
	cells := 0
	for _, s := range OpenUPS.Sensors {
		if strings.HasPrefix(s.Slug, "vcell_") {
			cells++
		}
	}
	if cells != 6 {
		tt.Errorf("%d cell sensors, want 6", cells)
	}
}

func TestOpenUPSOnBattery(t *testing.T) {
	d := &fakeDev{
		replies: map[byte][]byte{
			0x81: synth(32, func(b []byte) {
				b[0] = 0x82
				b[1], b[2] = 0x76, 0x01   // vin 13.26
				b[5], b[6] = 0x19, 0x09   // vbat 19.96
				b[21], b[22] = 0xa0, 0x00 // 2.578 A
				b[24] = 1 << 6            // running on battery
				b[31] = 0x19
			}),
			0x85: synth(32, func(b []byte) { b[0] = 0x86 }),
		},
	}
	r, err := OpenUPS.Poll(d, OpenUPS.Size)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "OB DISCHRG" {
		t.Errorf("status %q", r.Status)
	}
	wantStr(t, r, "power_state", "battery")
	wantNum(t, r, "c_discharge", 2.57808, 1e-3)
	wantNum(t, r, "c_in", 0, 1e-9)
	if got := r.Vars["battery.current"]; got != "2.58" {
		t.Errorf("battery.current = %q, want 2.58", got)
	}
}

func TestOpenUPSIdle(t *testing.T) {
	d := &fakeDev{
		replies: map[byte][]byte{
			0x81: synth(32, func(b []byte) { b[0] = 0x82; b[31] = 0x19 }),
			0x85: synth(32, func(b []byte) { b[0] = 0x86 }),
		},
	}
	r, err := OpenUPS.Poll(d, OpenUPS.Size)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "OL" {
		t.Errorf("status %q", r.Status)
	}
	wantStr(t, r, "power_state", "idle")
}

func TestOpenUPSRejectsBadReplies(t *testing.T) {
	cases := map[string]*fakeDev{
		"wrong type": {replies: map[byte][]byte{
			0x81: synth(32, func(b []byte) { b[0] = 0x00 }),
		}},
		"short report": {replies: map[byte][]byte{
			0x81: {0x82, 0x00, 0x01},
		}},
		"missing reply": {replies: map[byte][]byte{}},
	}
	for name, d := range cases {
		if _, err := OpenUPS.Poll(d, OpenUPS.Size); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}

	// A good 0x82 but a wrong second reply must also fail.
	d := &fakeDev{replies: map[byte][]byte{
		0x81: synth(32, func(b []byte) { b[0] = 0x82; b[31] = 0x19 }),
		0x85: synth(32, func(b []byte) { b[0] = 0x00 }),
	}}
	if _, err := OpenUPS.Poll(d, OpenUPS.Size); err == nil {
		t.Error("bad 0x86 must fail")
	}
}

func TestThermistor(t *testing.T) {
	if termistorToTemp(0) != -40 {
		t.Error("low clamp broken")
	}
	if termistorToTemp(0xFFFF) != 125 {
		t.Error("high clamp broken")
	}
	for i, raw := range termTable {
		if got := termistorToTemp(raw); got != float64(i*5-40) {
			t.Fatalf("table[%d]=%#x decoded as %v, want %d", i, raw, got, i*5-40)
		}
	}
	// 630 lies between table[15]=604 and table[16]=646 -> 35 + ceil(26*5/42) = 39
	if got := termistorToTemp(630); got != 39 {
		t.Errorf("630 = %v, want 39", got)
	}
}

func TestReadingValue(t *testing.T) {
	r := Reading{
		Firmware: "2.3",
		Num:      map[string]float64{"vin": 12.3456},
		Str:      map[string]string{"power_state": "idle"},
	}
	s := Sensor{Slug: "vin", Precision: 2}
	if got := r.Value(s); got != "12.35" {
		t.Errorf("vin = %q", got)
	}
	if got := r.Value(Sensor{Slug: "missing", Precision: 2}); got != "" {
		t.Errorf("missing = %q, want empty", got)
	}
	if got := r.Value(Sensor{Slug: "power_state", Text: true}); got != "idle" {
		t.Errorf("power_state = %q", got)
	}
	if got := r.Value(Sensor{Slug: "firmware", Text: true}); got != "2.3" {
		t.Errorf("firmware = %q", got)
	}
}

func TestRegistry(t *testing.T) {
	if len(Registry) != 4 {
		t.Fatalf("%d devices registered, want 4", len(Registry))
	}
	for _, d := range Registry {
		if d.Size != 32 && d.Size != 24 {
			t.Errorf("%s: report size %d", d.Model, d.Size)
		}
		if len(d.Requests) == 0 || len(d.Sensors) == 0 || d.Poll == nil {
			t.Errorf("%s: incomplete registration", d.Model)
		}
		if _, ok := DeviceByPID(d.PID); !ok {
			t.Errorf("%s: not found by pid", d.Model)
		}
		if _, ok := DeviceByName(d.Model); !ok {
			t.Errorf("%s: not found by name", d.Model)
		}
	}
	if _, ok := DeviceByPID(0x1234); ok {
		t.Error("unknown pid must not resolve")
	}
}
