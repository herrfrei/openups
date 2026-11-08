package main

import (
	"errors"
	"os"
	"testing"

	"github.com/herrfrei/openupsd/internal/core"
)

// flashDev serves 0xA1 page reads from a flash image, like the real device.
type flashDev struct {
	img  []byte
	fail bool
}

func (f *flashDev) Serial() string { return "TEST" }
func (f *flashDev) Close() error   { return nil }
func (f *flashDev) Exchange(cType byte, data, reply []byte) (int, error) {
	if f.fail {
		return 0, errors.New("usb gone")
	}
	if cType != 0xA1 {
		return 0, errors.New("unexpected request")
	}
	off := (int(data[0]) | int(data[1])<<8) - core.FlashStart
	for i := range reply {
		reply[i] = 0
	}
	reply[0] = 0xA2
	copy(reply[5:], f.img[off:off+core.FlashPage])
	return 32, nil
}

func realImage(t *testing.T) []byte {
	t.Helper()
	img, err := os.ReadFile("../../internal/core/testdata/openups_real_flash.bin")
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestWithEstimatorAddsSensorsOnARealImage(t *testing.T) {
	m, est := withEstimator(&core.OpenUPS, &flashDev{img: realImage(t)}, true, "")
	if est == nil {
		t.Fatal("no estimator for a valid OpenUPS configuration")
	}
	if len(m.Sensors) != len(core.OpenUPS.Sensors)+3 {
		t.Fatalf("sensors %d, want %d", len(m.Sensors), len(core.OpenUPS.Sensors)+3)
	}
	if len(core.OpenUPS.Sensors) == len(m.Sensors) {
		t.Fatal("the shared registry entry was modified")
	}
	if m.Poll == nil || m.PID != core.OpenUPS.PID {
		t.Fatal("derived device lost its poller")
	}
}

func TestWithEstimatorFallsBackQuietly(t *testing.T) {
	blank := make([]byte, core.FlashSize)
	for i := range blank {
		blank[i] = 0xFF
	}
	cases := map[string]struct {
		dev     *flashDev
		enabled bool
		model   *core.Device
	}{
		"disabled":         {&flashDev{img: realImage(t)}, false, &core.OpenUPS},
		"blank flash":      {&flashDev{img: blank}, true, &core.OpenUPS},
		"usb error":        {&flashDev{img: realImage(t), fail: true}, true, &core.OpenUPS},
		"experimental dev": {&flashDev{img: realImage(t)}, true, &core.OpenUPS2},
	}
	for name, c := range cases {
		m, est := withEstimator(c.model, c.dev, c.enabled, "")
		if est != nil || m != c.model {
			t.Errorf("%s: estimator or modified device returned", name)
		}
	}
}

func TestEstimateOptionDefaultsToOn(t *testing.T) {
	var o addonOptions
	if !o.estimateEnabled() {
		t.Error("estimate should default to on")
	}
	off := false
	o.BatteryEstimate = &off
	if o.estimateEnabled() {
		t.Error("estimate option ignored")
	}
}
