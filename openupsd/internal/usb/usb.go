// Package usb implements the Mini-Box HID transport (32-byte reports,
// 24 bytes for the DCDC-USB) on top of a platform-specific Transport.
package usb

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	RecvTimeout = 1000 * time.Millisecond // matches USBHID_RECV_TIMEOUT in pyopenups

	drainTimeout = 2 * time.Millisecond
	maxDrain     = 16
)

var (
	// ErrTimeout is returned by Transport.ReadInterrupt when nothing arrived in time.
	ErrTimeout = errors.New("usb: read timeout")
	// ErrNotFound is returned when no matching device is present.
	ErrNotFound = errors.New("usb: device not found")
)

// Transport moves already-framed, fixed-size reports.
type Transport interface {
	WriteInterrupt(frame []byte) error
	ReadInterrupt(buf []byte, timeout time.Duration) (int, error)
	Close() error
}

type DeviceID struct {
	Vendor  uint16
	Product uint16
	MaxIn   int    // report size: 32 (OpenUPS/OpenUPS2), 24 (DCDC-USB)
	Path    string // optional: explicit usbfs node such as /dev/bus/usb/003/009 (skips enumeration)
}

type Dev interface {
	// Exchange sends one request and reads one reply atomically
	// (drains stale input first). reply must be >= the report size.
	Exchange(cType byte, data, reply []byte) (int, error)
	Serial() string
	Close() error
}

// Frame builds exactly what pyopenups sends: [type]+payload, zero-padded.
// Unlike Python (which truncates with a warning) it fails on overlong input.
func Frame(cType byte, data []byte, size int) ([]byte, error) {
	if 1+len(data) > size {
		return nil, fmt.Errorf("message of %d bytes exceeds report size %d", 1+len(data), size)
	}
	f := make([]byte, size)
	f[0] = cType
	copy(f[1:], data)
	return f, nil
}

type dev struct {
	t      Transport
	size   int
	serial string
	mu     sync.Mutex // one request/response at a time
}

func newDev(t Transport, size int, serial string) *dev {
	return &dev{t: t, size: size, serial: serial}
}

func (d *dev) send(cType byte, data []byte) error {
	f, err := Frame(cType, data, d.size)
	if err != nil {
		return err
	}
	return d.t.WriteInterrupt(f)
}

func (d *dev) recv(buf []byte) (int, error) {
	if len(buf) < d.size {
		return 0, fmt.Errorf("receive buffer %d bytes is smaller than report size %d", len(buf), d.size)
	}
	n, err := d.t.ReadInterrupt(buf[:d.size], RecvTimeout)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrTimeout // an empty read is not a reply
	}
	return n, nil
}

// drain discards reports that arrived unsolicited, so they cannot be
// mistaken for the reply to the next request.
func (d *dev) drain() {
	buf := make([]byte, d.size)
	for i := 0; i < maxDrain; i++ {
		if _, err := d.t.ReadInterrupt(buf, drainTimeout); err != nil {
			return
		}
	}
}

func (d *dev) Exchange(cType byte, data, reply []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.drain()
	if err := d.send(cType, data); err != nil {
		return 0, err
	}
	return d.recv(reply)
}

func (d *dev) Serial() string { return d.serial }
func (d *dev) Close() error   { return d.t.Close() }
