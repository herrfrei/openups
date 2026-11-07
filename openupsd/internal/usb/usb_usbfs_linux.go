//go:build linux

package usb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	epIn       = 0x81 // same as pyopenups usbhid.py
	epOut      = 0x01
	interface0 = 0

	usbdevfsDisconnect = 0x5516 // _IO('U', 22)
)

type bulkTransfer struct {
	ep      uint32
	length  uint32
	timeout uint32 // ms
	data    unsafe.Pointer
}

type usbIoctl struct {
	ifno int32
	code int32
	data unsafe.Pointer
}

// struct usbdevfs_ctrltransfer from the kernel's usbdevice_fs.h
type ctrlTransfer struct {
	bRequestType uint8
	bRequest     uint8
	wValue       uint16
	wIndex       uint16
	wLength      uint16
	timeout      uint32 // ms
	data         unsafe.Pointer
}

// _IOC(dir, type, nr, size)
func ioc(dir, typ, nr, size uintptr) uintptr {
	return dir<<30 | size<<16 | typ<<8 | nr
}

var (
	usbdevfsControl = ioc(3, 'U', 0, unsafe.Sizeof(ctrlTransfer{})) // _IOWR
	usbdevfsBulk    = ioc(3, 'U', 2, unsafe.Sizeof(bulkTransfer{})) // _IOWR
	usbdevfsIoctl   = ioc(3, 'U', 18, unsafe.Sizeof(usbIoctl{}))    // _IOWR
	usbdevfsClaim   = ioc(2, 'U', 15, 4)                            // _IOR unsigned int
	usbdevfsRelease = ioc(2, 'U', 16, 4)                            // _IOR unsigned int
)

func ioctl(fd int, req uintptr, arg unsafe.Pointer) (int, error) {
	r, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	if e != 0 {
		return 0, e
	}
	return int(r), nil
}

type usbfsInfo struct {
	path string
	bus  int
	addr int
}

// parseDeviceDescriptor returns VID/PID from the first 18 bytes of a usbfs node.
func parseDeviceDescriptor(b []byte) (vid, pid uint16, ok bool) {
	if len(b) < 12 || b[0] != 18 || b[1] != 1 {
		return 0, 0, false
	}
	return uint16(b[8]) | uint16(b[9])<<8, uint16(b[10]) | uint16(b[11])<<8, true
}

// enumerateUSB lists matching devices ordered by (bus, address), like pyopenups.
func enumerateUSB(vid, pid uint16) ([]usbfsInfo, error) {
	paths, err := filepath.Glob("/dev/bus/usb/[0-9]*/[0-9]*")
	if err != nil {
		return nil, err
	}
	var out []usbfsInfo
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		b := make([]byte, 18)
		n, _ := f.Read(b)
		f.Close()
		v, pr, ok := parseDeviceDescriptor(b[:n])
		if !ok || v != vid || pr != pid {
			continue
		}
		bus, _ := strconv.Atoi(filepath.Base(filepath.Dir(p)))
		addr, _ := strconv.Atoi(filepath.Base(p))
		out = append(out, usbfsInfo{path: p, bus: bus, addr: addr})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].bus != out[b].bus {
			return out[a].bus < out[b].bus
		}
		return out[a].addr < out[b].addr
	})
	return out, nil
}

// Probe reads the device descriptor of an explicit usbfs node.
func Probe(path string) (vid, pid uint16, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	b := make([]byte, 18)
	n, err := f.Read(b)
	if err != nil {
		return 0, 0, fmt.Errorf("read descriptor of %s: %w", path, err)
	}
	v, p, ok := parseDeviceDescriptor(b[:n])
	if !ok {
		return 0, 0, fmt.Errorf("%s: not a USB device node", path)
	}
	return v, p, nil
}

// stringDescriptor fetches a USB string descriptor (bDescriptorType 0x03).
// The result is restricted to the ASCII range: these devices only ever carry
// ASCII serials, and a full UTF-16 decoder would be dead weight here.
func stringDescriptor(fd int, index byte) (string, error) {
	var buf [255]byte
	ct := ctrlTransfer{
		bRequestType: 0x80, // device-to-host, standard, device
		bRequest:     0x06, // GET_DESCRIPTOR
		wValue:       uint16(0x03)<<8 | uint16(index),
		wIndex:       0x0409, // en-US
		wLength:      uint16(len(buf)),
		timeout:      1000,
		data:         unsafe.Pointer(&buf[0]),
	}
	n, err := ioctl(fd, usbdevfsControl, unsafe.Pointer(&ct))
	if err != nil {
		return "", err
	}
	if n < 2 || buf[1] != 0x03 {
		return "", errors.New("usb: not a string descriptor")
	}
	out := make([]byte, 0, (n-2)/2)
	for i := 2; i+1 < n; i += 2 {
		if buf[i] == 0 {
			break
		}
		out = append(out, buf[i])
	}
	return strings.TrimSpace(string(out)), nil
}

// readSerial returns iSerialNumber from the device descriptor, or "" when the
// device has none or the descriptor cannot be read. Failure is not an error:
// the serial is cosmetic (device name in HA, device.serial in NUT).
func readSerial(devPath string, fd int) string {
	f, err := os.Open(devPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 18)
	n, err := f.Read(head)
	if err != nil || n < 18 || head[16] == 0 {
		return "" // no iSerialNumber
	}
	s, err := stringDescriptor(fd, head[16])
	if err != nil {
		return ""
	}
	return s
}

type usbfsTransport struct{ f *os.File }

func (t *usbfsTransport) bulk(ep uint32, buf []byte, timeout time.Duration) (int, error) {
	if len(buf) == 0 {
		return 0, errors.New("usbfs: empty buffer")
	}
	ms := uint32(timeout / time.Millisecond)
	if ms == 0 {
		ms = 1
	}
	bt := bulkTransfer{ep: ep, length: uint32(len(buf)), timeout: ms, data: unsafe.Pointer(&buf[0])}
	n, err := ioctl(int(t.f.Fd()), usbdevfsBulk, unsafe.Pointer(&bt))
	if err != nil {
		if errors.Is(err, syscall.ETIMEDOUT) {
			return 0, ErrTimeout
		}
		return 0, err
	}
	return n, nil
}

// WriteInterrupt sends the frame raw, with no report-ID prefix: this is exactly
// what pyusb does with device.write(0x01, packet).
func (t *usbfsTransport) WriteInterrupt(frame []byte) error {
	n, err := t.bulk(epOut, append([]byte(nil), frame...), RecvTimeout)
	if err != nil {
		return fmt.Errorf("usbfs write: %w", err)
	}
	if n != len(frame) {
		return fmt.Errorf("short usbfs write: %d of %d", n, len(frame))
	}
	return nil
}

func (t *usbfsTransport) ReadInterrupt(buf []byte, timeout time.Duration) (int, error) {
	n, err := t.bulk(epIn, buf, timeout)
	if errors.Is(err, ErrTimeout) {
		return 0, ErrTimeout
	}
	if err != nil {
		return 0, fmt.Errorf("usbfs read: %w", err)
	}
	return n, nil
}

func (t *usbfsTransport) Close() error {
	ifno := uint32(interface0)
	_, _ = ioctl(int(t.f.Fd()), usbdevfsRelease, unsafe.Pointer(&ifno))
	return t.f.Close()
}

// OpenUSB opens the index-th matching device through usbfs, or id.Path
// (e.g. /dev/bus/usb/003/009) directly if set.
func OpenUSB(ctx context.Context, id DeviceID, index int) (Dev, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id.MaxIn <= 0 {
		return nil, errors.New("DeviceID.MaxIn (report size) must be set")
	}
	path := id.Path
	if path == "" {
		infos, err := enumerateUSB(id.Vendor, id.Product)
		if err != nil {
			return nil, fmt.Errorf("enumerate: %w", err)
		}
		if index < 0 || index >= len(infos) {
			return nil, fmt.Errorf("%w: %04x:%04x index %d (%d present)",
				ErrNotFound, id.Vendor, id.Product, index, len(infos))
		}
		path = infos[index].path
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w (device mapped into the container? permissions?)", path, err)
	}
	fd := int(f.Fd())

	// Detach a bound kernel driver (usbhid); ENODATA means none is bound.
	uio := usbIoctl{ifno: interface0, code: usbdevfsDisconnect}
	_, _ = ioctl(fd, usbdevfsIoctl, unsafe.Pointer(&uio))

	ifno := uint32(interface0)
	if _, err := ioctl(fd, usbdevfsClaim, unsafe.Pointer(&ifno)); err != nil {
		f.Close()
		return nil, fmt.Errorf("claim interface %d on %s: %w", interface0, path, err)
	}

	serial := readSerial(path, fd)
	return newDev(&usbfsTransport{f: f}, id.MaxIn, serial), nil
}
