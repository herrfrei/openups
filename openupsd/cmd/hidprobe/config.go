package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/herrfrei/openupsd/internal/core"
	"github.com/herrfrei/openupsd/internal/usb"
)

// flashOpts holds the read-only settings dump options.
type flashOpts struct {
	dump    bool
	desc    bool
	textOut string
	binOut  string
}

func (o *flashOpts) register() {
	flag.BoolVar(&o.dump, "config", false, "OpenUPS: read the settings flash and print every parameter (read-only)")
	flag.BoolVar(&o.desc, "config-desc", false, "with -config: also print each parameter's description")
	flag.StringVar(&o.textOut, "config-out", "", "with -config: write NAME=value lines to this file (same layout as the C++ tool)")
	flag.StringVar(&o.binOut, "flash-out", "", "with -config: save the raw 1 KiB flash image to this file")
}

func (o *flashOpts) active() bool { return o.dump }

// runFlash prints the OpenUPS settings. It never writes to the device.
func runFlash(d usb.Dev, dev *core.Device, o *flashOpts) error {
	if dev == nil || dev.PID != core.OpenUPS.PID {
		return errors.New("flash access is only implemented for the OpenUPS (pid 0xd004)")
	}
	img, err := core.ReadFlash(d)
	if err != nil {
		return fmt.Errorf("reading flash: %w", err)
	}
	if err := sanityCheck(img); err != nil {
		return err
	}
	if err := printConfig(img, o.desc); err != nil {
		return err
	}
	fmt.Printf("\nflash map (16 byte pages, '.' blank, '#' data):\n  %s\n", core.PageMap(img))
	if o.textOut != "" {
		if err := os.WriteFile(o.textOut, []byte(textDump(img)), 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s\n", o.textOut)
	}
	if o.binOut != "" {
		if err := os.WriteFile(o.binOut, img, 0o600); err != nil {
			return err
		}
		fmt.Printf("wrote %s (%d bytes)\n", o.binOut, len(img))
	}
	return nil
}

// sanityCheck refuses images that cannot be a real configuration (an erased
// or unreadable device).
func sanityCheck(img []byte) error {
	if len(img) != core.FlashSize {
		return fmt.Errorf("flash image is %d bytes, expected %d", len(img), core.FlashSize)
	}
	if bytes.Count(img, []byte{0xFF}) == len(img) {
		return errors.New("flash reads back as all 0xFF (erased)")
	}
	if bytes.Count(img, []byte{0x00}) == len(img) {
		return errors.New("flash reads back as all zero; the read did not work")
	}
	return nil
}

func printConfig(img []byte, desc bool) error {
	fmt.Printf("%-24s %-14s %-8s %s\n", "parameter", "value", "unit", "offset")
	for _, p := range core.OpenUPSParams {
		v, err := p.Decode(img)
		if err != nil {
			return err
		}
		fmt.Printf("%-24s %-14s %-8s %4d\n", p.Name, v, strings.Trim(p.Unit, "[]"), p.Index)
		if desc {
			for _, line := range strings.Split(p.Desc, "\n") {
				fmt.Printf("    # %s\n", line)
			}
		}
	}
	return nil
}

func textDump(img []byte) string {
	var sb strings.Builder
	for _, p := range core.OpenUPSParams {
		if v, err := p.Decode(img); err == nil {
			fmt.Fprintf(&sb, "%s=%s\n", p.Name, v)
		}
	}
	return sb.String()
}
