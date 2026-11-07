package mqtt

import (
	"testing"

	"github.com/herrfrei/openupsd/internal/core"
)

func newTestBridge(t *testing.T) *Bridge {
	t.Helper()
	b, err := New(Config{Broker: "127.0.0.1", NodeID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func find(t *testing.T, d *core.Device, slug string) core.Sensor {
	t.Helper()
	for _, s := range d.Sensors {
		if s.Slug == slug {
			return s
		}
	}
	t.Fatalf("%s: sensor %q not found", d.Model, slug)
	return core.Sensor{}
}

func TestTopicLayout(t *testing.T) {
	b := newTestBridge(t)
	s := find(t, &core.OpenUPS, "vin")
	if got, want := b.stateTopic(s), "openups/test/sensor/vin/state"; got != want {
		t.Errorf("state topic %q, want %q", got, want)
	}
	if got, want := b.discoveryTopic(s), "homeassistant/sensor/test/vin/config"; got != want {
		t.Errorf("discovery topic %q, want %q", got, want)
	}
	if b.avail != "openups/test/availability" {
		t.Errorf("availability %q", b.avail)
	}
}

func TestDiscoveryPayload(t *testing.T) {
	b := newTestBridge(t)
	r := core.Reading{Firmware: "1.9"}
	p := b.discoveryPayload(find(t, &core.OpenUPS, "vin"), b.devicePayload(&core.OpenUPS, r, "SN1"))

	want := map[string]any{
		"name":                        "VIN",
		"unique_id":                   "test_vin",
		"state_topic":                 "openups/test/sensor/vin/state",
		"availability_topic":          "openups/test/availability",
		"payload_available":           "online",
		"payload_not_available":       "offline",
		"device_class":                "voltage",
		"state_class":                 "measurement",
		"unit_of_measurement":         "V",
		"expire_after":                statusExpire,
		"suggested_display_precision": 2,
	}
	for k, w := range want {
		if got := p[k]; got != w {
			t.Errorf("%s = %v, want %v", k, got, w)
		}
	}
}

func TestDiscoveryCarriesPrecision(t *testing.T) {
	b := newTestBridge(t)
	cases := map[string]int{
		"vin": 2, "vout": 2, "vbat": 2, "c_in": 2,
		"temperature": 1, "output_power": 1, "vcell_1": 3,
	}
	for slug, want := range cases {
		p := b.discoveryPayload(find(t, &core.OpenUPS, slug), b.devicePayload(&core.OpenUPS, core.Reading{}, ""))
		if got := p["suggested_display_precision"]; got != want {
			t.Errorf("%s: suggested_display_precision = %v, want %d", slug, got, want)
		}
	}
	for _, slug := range []string{"power_state", "firmware"} {
		p := b.discoveryPayload(find(t, &core.OpenUPS, slug), b.devicePayload(&core.OpenUPS, core.Reading{}, ""))
		if _, ok := p["suggested_display_precision"]; ok {
			t.Errorf("%s must not carry a precision", slug)
		}
	}
}

func TestTextEntitiesHaveNoPrecision(t *testing.T) {
	b := newTestBridge(t)
	for _, d := range core.Registry {
		for _, s := range d.Sensors {
			if !s.Text {
				continue
			}
			p := b.discoveryPayload(s, b.devicePayload(d, core.Reading{}, ""))
			if _, ok := p["suggested_display_precision"]; ok {
				t.Errorf("%s/%s: text entity must not carry a precision", d.Model, s.Slug)
			}
			if _, ok := p["unit_of_measurement"]; ok {
				t.Errorf("%s/%s: text entity must not carry a unit", d.Model, s.Slug)
			}
		}
	}
}

func TestDevicePayload(t *testing.T) {
	b := newTestBridge(t)
	d := b.devicePayload(&core.OpenUPS, core.Reading{Firmware: "1.9"}, "SN1")
	if d["sw_version"] != "1.9" || d["serial_number"] != "SN1" {
		t.Errorf("unexpected device payload: %v", d)
	}
	if d["name"] != "OpenUPS (SN1)" || d["model"] != "OpenUPS" || d["manufacturer"] != "Mini-Box.com" {
		t.Errorf("unexpected device payload: %v", d)
	}
	ids, ok := d["identifiers"].([]string)
	if !ok || len(ids) != 1 || ids[0] != "test" {
		t.Errorf("identifiers: %v", d["identifiers"])
	}
}

func TestDevicePayloadWithoutSerial(t *testing.T) {
	b := newTestBridge(t)
	d := b.devicePayload(&core.OpenUPS2, core.Reading{}, "")
	if d["name"] != "OpenUPS2" {
		t.Errorf("name %v", d["name"])
	}
	for _, k := range []string{"serial_number", "sw_version"} {
		if _, ok := d[k]; ok {
			t.Errorf("%s must be absent", k)
		}
	}
}

func TestDiagnosticStillExpires(t *testing.T) {
	b := newTestBridge(t)
	p := b.discoveryPayload(find(t, &core.OpenUPS, "firmware"), b.devicePayload(&core.OpenUPS, core.Reading{}, ""))
	if p["entity_category"] != "diagnostic" {
		t.Errorf("entity_category = %v", p["entity_category"])
	}
	if p["expire_after"] != statusExpire {
		t.Errorf("expire_after = %v", p["expire_after"])
	}
}

func TestSensorValues(t *testing.T) {
	r := core.Reading{
		Firmware: "1.9",
		Status:   "OL",
		Num: map[string]float64{
			"vin": 13.2583, "vout": 12.058, "vbat": 19.9595,
			"c_charge": 0, "c_discharge": 2.578, "c_in": 2.578,
			"temperature": 39, "output_power": 33.64999,
			"vcell_1": 3.308, "vcell_6": 3.334,
		},
		Str: map[string]string{"power_state": "charging"},
	}
	want := map[string]string{
		"vin": "13.26", "vout": "12.06", "vbat": "19.96",
		"c_charge": "0.00", "c_discharge": "2.58", "c_in": "2.58",
		"temperature": "39.0", "output_power": "33.6", // 33.64999 rounds down
		"power_state": "charging", "firmware": "1.9",
		"vcell_1": "3.308", "vcell_6": "3.334",
	}
	for _, s := range core.OpenUPS.Sensors {
		w, ok := want[s.Slug]
		if !ok {
			continue
		}
		if got := r.Value(s); got != w {
			t.Errorf("%s = %q, want %q", s.Slug, got, w)
		}
		delete(want, s.Slug)
	}
	for slug := range want {
		t.Errorf("sensor %q not found", slug)
	}
}

func TestMissingValueIsSkipped(t *testing.T) {
	r := core.Reading{Num: map[string]float64{}}
	if got := r.Value(find(t, &core.OpenUPS2, "capacity")); got != "" {
		t.Errorf("capacity = %q, want empty so Publish skips it", got)
	}
}
