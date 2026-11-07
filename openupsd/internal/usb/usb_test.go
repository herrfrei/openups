package usb

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func golden(prefix string, size int) string {
	return prefix + strings.Repeat("00", size-len(prefix)/2)
}

func TestFrameMatchesPython(t *testing.T) {
	cases := []struct {
		name string
		typ  byte
		data []byte
		size int
		want string
	}{
		{"openups GET_ALL_VALUES", 0x81, nil, 32, golden("81", 32)},
		{"openups GET_ALL_VALUES_2", 0x85, nil, 32, golden("85", 32)},
		{"openups restart (send_command AA,1)", 0xB1, []byte{0xAA, 1, 0}, 32, golden("b1aa0100", 32)},
		{"openups read page 0x7800", 0xA1, []byte{0x00, 0x78, 0x00, 16}, 32, golden("a100780010", 32)},
		{"openups2 bootloader", 0xA9, []byte{0xFA, 0xCD}, 32, golden("a9facd", 32)},
		{"dcdc GET_ALL_VALUES", 0x81, nil, 24, golden("81", 24)},
		{"dcdc read VOUT", 0xB1, []byte{0x07, 0, 0}, 24, golden("b1070000", 24)},
		{"dcdc read page 0x3FC0", 0xA1, []byte{0xC0, 0x3F, 0x00, 16}, 24, golden("a1c03f0010", 24)},
	}
	for _, c := range cases {
		f, err := Frame(c.typ, c.data, c.size)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := hex.EncodeToString(f); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, got, c.want)
		}
	}
}

func TestFrameRejectsOverlong(t *testing.T) {
	if _, err := Frame(0xA3, make([]byte, 32), 32); err == nil {
		t.Fatal("expected error")
	}
}

// fakeTransport returns stale reports first, then a reply once a frame was written.
type fakeTransport struct {
	stale   [][]byte
	reply   []byte
	wrote   bool
	written [][]byte
}

func (f *fakeTransport) WriteInterrupt(fr []byte) error {
	f.written = append(f.written, append([]byte(nil), fr...))
	f.wrote = true
	return nil
}

func (f *fakeTransport) ReadInterrupt(buf []byte, _ time.Duration) (int, error) {
	if len(f.stale) > 0 {
		n := copy(buf, f.stale[0])
		f.stale = f.stale[1:]
		return n, nil
	}
	if f.wrote {
		f.wrote = false
		return copy(buf, f.reply), nil
	}
	return 0, ErrTimeout
}

func (f *fakeTransport) Close() error { return nil }

func TestExchangeDrainsStaleAndFrames(t *testing.T) {
	ft := &fakeTransport{
		stale: [][]byte{{0xEE, 0x01}},
		reply: []byte{0x82, 0x11, 0x22},
	}
	d := newDev(ft, 32, "TEST")
	buf := make([]byte, 32)
	n, err := d.Exchange(0x81, nil, buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 || buf[0] != 0x82 {
		t.Fatalf("got n=%d buf[0]=%#x, want the real reply (stale report not drained)", n, buf[0])
	}
	if len(ft.written) != 1 || hex.EncodeToString(ft.written[0]) != golden("81", 32) {
		t.Fatalf("unexpected frame written: %x", ft.written)
	}
}

func TestExchangeRejectsSmallBuffer(t *testing.T) {
	d := newDev(&fakeTransport{}, 32, "")
	if _, err := d.Exchange(0x81, nil, make([]byte, 8)); err == nil {
		t.Fatal("expected error for undersized buffer")
	}
}

func TestExchangeTimeout(t *testing.T) {
	d := newDev(&fakeTransport{}, 32, "") // no reply queued -> timeout
	if _, err := d.Exchange(0x81, nil, make([]byte, 32)); err != ErrTimeout {
		t.Fatalf("got %v, want ErrTimeout", err)
	}
}
