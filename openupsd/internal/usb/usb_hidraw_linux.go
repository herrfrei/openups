//go:build linux

package usb

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type hidrawInfo struct {
	num    int
	path   string
	serial string
}

// parseUevent extracts VID/PID and serial from a hidraw device's uevent file.
// HID_ID has the form BBBB:VVVVVVVV:PPPPPPPP (bus, vendor, product).
func parseUevent(r io.Reader) (vid, pid uint16, uniq string, ok bool) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, found := strings.Cut(sc.Text(), "=")
		if !found {
			continue
		}
		switch k {
		case "HID_ID":
			p := strings.Split(v, ":")
			if len(p) != 3 {
				continue
			}
			a, e1 := strconv.ParseUint(p[1], 16, 32)
			b, e2 := strconv.ParseUint(p[2], 16, 32)
			if e1 == nil && e2 == nil {
				vid, pid, ok = uint16(a), uint16(b), true
			}
		case "HID_UNIQ":
			uniq = v
		}
	}
	return vid, pid, uniq, ok
}

// enumerate lists /dev/hidrawN nodes matching vid/pid, ordered by N.
func enumerate(vid, pid uint16) ([]hidrawInfo, error) {
	dirs, err := filepath.Glob("/sys/class/hidraw/hidraw*")
	if err != nil {
		return nil, err
	}
	var out []hidrawInfo
	for _, d := range dirs {
		name := filepath.Base(d)
		num, err := strconv.Atoi(strings.TrimPrefix(name, "hidraw"))
		if err != nil {
			continue
		}
		f, err := os.Open(filepath.Join(d, "device", "uevent"))
		if err != nil {
			continue
		}
		v, p, uniq, ok := parseUevent(f)
		f.Close()
		if ok && v == vid && p == pid {
			out = append(out, hidrawInfo{num: num, path: "/dev/" + name, serial: uniq})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].num < out[b].num })
	return out, nil
}

type hidrawTransport struct{ f *os.File }

// The devices use unnumbered reports: hidraw expects a leading 0x00 report ID
// and strips it before the data reaches the device.
func (t *hidrawTransport) WriteInterrupt(frame []byte) error {
	out := make([]byte, 1+len(frame))
	copy(out[1:], frame)
	if err := t.f.SetWriteDeadline(time.Now().Add(RecvTimeout)); err != nil {
		return fmt.Errorf("hidraw set write deadline: %w", err)
	}
	n, err := t.f.Write(out)
	if err != nil {
		return fmt.Errorf("hidraw write: %w", err)
	}
	// Depending on kernel version the count may or may not include the ID byte.
	if n < len(frame) {
		return fmt.Errorf("short hidraw write: %d of %d", n, len(frame))
	}
	return nil
}

func (t *hidrawTransport) ReadInterrupt(buf []byte, timeout time.Duration) (int, error) {
	if err := t.f.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return 0, fmt.Errorf("hidraw set read deadline (fd not pollable?): %w", err)
	}
	n, err := t.f.Read(buf)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return 0, ErrTimeout
	}
	if err != nil {
		return 0, fmt.Errorf("hidraw read: %w", err)
	}
	return n, nil
}

func (t *hidrawTransport) Close() error { return t.f.Close() }

// OpenHID opens the index-th matching device (ordered by hidraw number),
// or id.Path directly if set.
func OpenHID(ctx context.Context, id DeviceID, index int) (Dev, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id.MaxIn <= 0 {
		return nil, errors.New("DeviceID.MaxIn (report size) must be set")
	}

	path, serial := id.Path, ""
	if path == "" {
		infos, err := enumerate(id.Vendor, id.Product)
		if err != nil {
			return nil, fmt.Errorf("enumerate: %w", err)
		}
		if index < 0 || index >= len(infos) {
			return nil, fmt.Errorf("%w: %04x:%04x index %d (%d present)",
				ErrNotFound, id.Vendor, id.Product, index, len(infos))
		}
		path, serial = infos[index].path, infos[index].serial
	}

	// O_NONBLOCK lets the runtime poller handle the fd, which makes
	// SetReadDeadline/SetWriteDeadline work.
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w (permissions? no hidraw node?)", path, err)
	}
	return newDev(&hidrawTransport{f: f}, id.MaxIn, serial), nil
}
