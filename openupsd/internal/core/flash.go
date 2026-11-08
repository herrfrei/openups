package core

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/herrfrei/openupsd/internal/usb"
)

// The OpenUPS keeps its settings in 1 KiB of flash that the host reads in
// 16 byte pages with the same HID reports as the status data.
//
// This package only reads the flash. 
const (
	FlashStart = 0x7800
	FlashSize  = 0x400
	FlashPage  = 16

	opMemReadOut = 0xA1
	opMemReadIn  = 0xA2

	flashReplySize = 32 // OpenUPS report size
)

func flashAddr(off int) (lo, hi byte) {
	a := FlashStart + off
	return byte(a), byte(a >> 8)
}

func checkPageOffset(off int) error {
	if off < 0 || off%FlashPage != 0 || off+FlashPage > FlashSize {
		return fmt.Errorf("invalid flash page offset %d", off)
	}
	return nil
}

// ReadFlashPage reads one 16 byte page at the given offset into the settings
// area. It is read-only and safe to run at any time.
func ReadFlashPage(d usb.Dev, off int) ([]byte, error) {
	if err := checkPageOffset(off); err != nil {
		return nil, err
	}
	lo, hi := flashAddr(off)
	reply := make([]byte, flashReplySize)
	n, err := d.Exchange(opMemReadOut, []byte{lo, hi, 0x00, FlashPage}, reply)
	if err != nil {
		return nil, fmt.Errorf("read flash page 0x%04x: %w", FlashStart+off, err)
	}
	if n < 5+FlashPage || reply[0] != opMemReadIn {
		return nil, fmt.Errorf("read flash page 0x%04x: unexpected reply %#02x (%d bytes)",
			FlashStart+off, replyType(reply[:n]), n)
	}
	page := make([]byte, FlashPage)
	copy(page, reply[5:5+FlashPage])
	return page, nil
}

// ReadFlash reads the whole settings area (64 page reads).
func ReadFlash(d usb.Dev) ([]byte, error) {
	img := make([]byte, 0, FlashSize)
	for off := 0; off < FlashSize; off += FlashPage {
		var page []byte
		var err error
		for try := 0; try < 3; try++ { // a lost report is not worth failing a dump
			if page, err = ReadFlashPage(d, off); err == nil {
				break
			}
		}
		if err != nil {
			return nil, err
		}
		img = append(img, page...)
	}
	return img, nil
}

// PageMap draws one character per 16 byte page: '.' for a blank (all 0xFF)
// page, '#' for a page holding data.
func PageMap(img []byte) string {
	blank := bytes.Repeat([]byte{0xFF}, FlashPage)
	var sb strings.Builder
	for off := 0; off+FlashPage <= len(img); off += FlashPage {
		if bytes.Equal(img[off:off+FlashPage], blank) {
			sb.WriteByte('.')
		} else {
			sb.WriteByte('#')
		}
	}
	return sb.String()
}
