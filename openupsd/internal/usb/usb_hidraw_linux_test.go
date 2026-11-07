//go:build linux

package usb

import (
	"strings"
	"testing"
)

func TestParseUevent(t *testing.T) {
	const sample = `DRIVER=hid-generic
HID_ID=0003:000004D8:0000D004
HID_NAME=Mini-Box.com USB UPS
HID_PHYS=usb-0000:00:14.0-2/input0
HID_UNIQ=ABC123
MODALIAS=hid:b0003g0001v000004D8p0000D004
`
	vid, pid, uniq, ok := parseUevent(strings.NewReader(sample))
	if !ok || vid != 0x04D8 || pid != 0xD004 || uniq != "ABC123" {
		t.Fatalf("got %04x:%04x %q ok=%v", vid, pid, uniq, ok)
	}
}

func TestParseUeventNoHIDID(t *testing.T) {
	if _, _, _, ok := parseUevent(strings.NewReader("DRIVER=usbhid\n")); ok {
		t.Fatal("expected ok=false")
	}
}
