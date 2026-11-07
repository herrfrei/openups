package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"sort"

	"github.com/herrfrei/openupsd/internal/core"
	"github.com/herrfrei/openupsd/internal/usb"
)

func main() {
	pid := flag.Uint("pid", 0xD004, "product id (0xd003 dcdcusb, 0xd004 openups, 0xd005 openups2, 0xd007 nucups)")
	size := flag.Int("size", 32, "report size; taken from the device table when the pid is known (24 for dcdcusb)")
	idx := flag.Int("n", 0, "device index")
	path := flag.String("dev", "", "explicit usbfs node, e.g. /dev/bus/usb/003/009")
	mode := flag.String("mode", "usbfs", "transport: usbfs (like pyusb) or hidraw")
	decode := flag.Bool("decode", false, "run the model decoder and print the resulting values instead of raw reports")
	flag.Parse()

	id := usb.DeviceID{Vendor: 0x04D8, Product: uint16(*pid), MaxIn: *size, Path: *path}
	dev, known := core.DeviceByPID(uint16(*pid))
	if known {
		id.MaxIn = dev.Size
	}

	var d usb.Dev
	var err error
	if *mode == "hidraw" {
		d, err = usb.OpenHID(context.Background(), id, *idx)
	} else {
		d, err = usb.OpenUSB(context.Background(), id, *idx)
	}
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()

	if !known {
		fmt.Printf("product %04x is not in the device table, using a generic probe\n", uint16(*pid))
	}
	fmt.Printf("serial: %q\n", d.Serial())

	if *decode {
		if !known {
			log.Fatal("-decode needs a known product id")
		}
		r, err := dev.Poll(d, dev.Size)
		if err != nil {
			log.Fatalf("decode: %v", err)
		}
		fmt.Printf("firmware: %s\n", r.Firmware)
		fmt.Printf("status:   %s\n", r.Status)
		for _, s := range dev.Sensors {
			v := r.Value(s)
			if v == "" {
				v = "-"
			}
			fmt.Printf("%-20s %-3s %s\n", s.Slug, s.Unit, v)
		}
		keys := make([]string, 0, len(r.Vars))
		for k := range r.Vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("nut: %-22s %s\n", k, r.Vars[k])
		}
		return
	}

	// Raw mode: every request the decoder uses, so new models can be captured.
	reqs := []byte{0x81, 0x85}
	if known {
		reqs = dev.Requests
	}
	for _, req := range reqs {
		var data []byte
		if req == 0xB1 {
			data = []byte{0x07, 0x00, 0x00} // DCDC-USB: read configured VOut
		}
		f, err := usb.Frame(req, data, id.MaxIn)
		if err != nil {
			log.Fatalf("frame %#02x: %v", req, err)
		}
		fmt.Printf("TX %s\n", hex.EncodeToString(f))

		buf := make([]byte, id.MaxIn)
		n, err := d.Exchange(req, data, buf)
		if err != nil {
			log.Fatalf("request %#02x: %v", req, err)
		}
		status := "ok"
		if want := req + 1; n == 0 || buf[0] != want {
			status = fmt.Sprintf("UNEXPECTED (wanted %#02x)", want)
		}
		fmt.Printf("RX %s  [%d bytes, type %#02x %s]\n", hex.EncodeToString(buf[:n]), n, buf[0], status)
	}
}
