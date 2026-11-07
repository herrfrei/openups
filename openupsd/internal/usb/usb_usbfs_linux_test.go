//go:build linux

package usb

import "testing"

func TestParseDeviceDescriptor(t *testing.T) {
	// bLength=18, bDescriptorType=1, ..., idVendor=04D8, idProduct=D004 (little endian)
	d := []byte{18, 1, 0x00, 0x02, 0, 0, 0, 64, 0xD8, 0x04, 0x04, 0xD0, 0, 0, 1, 2, 3, 1}
	v, p, ok := parseDeviceDescriptor(d)
	if !ok || v != 0x04D8 || p != 0xD004 {
		t.Fatalf("got %04x:%04x ok=%v", v, p, ok)
	}
	if _, _, ok := parseDeviceDescriptor(d[:5]); ok {
		t.Fatal("short descriptor must fail")
	}
}
