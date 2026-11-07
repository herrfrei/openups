package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/herrfrei/openupsd/internal/core"
	"github.com/herrfrei/openupsd/internal/mqtt"
	"github.com/herrfrei/openupsd/internal/nut"
	"github.com/herrfrei/openupsd/internal/usb"
)

const (
	maxFailures = 3
	optionsPath = "/data/options.json"
	openDelay   = 100 * time.Millisecond // same settle time as the C++ main()
	vendorID    = 0x04D8
)

// addonOptions mirrors the schema in config.yaml. Fields missing from the file
// keep their defaults, so the binary also runs without an add-on.
type addonOptions struct {
	MQTTHost     string `json:"mqtt_host"`
	MQTTPort     int    `json:"mqtt_port"`
	MQTTUsername string `json:"mqtt_username"`
	MQTTPassword string `json:"mqtt_password"`
	MQTTPrefix   string `json:"mqtt_prefix"`
	NodeID       string `json:"node_id"`
	Model        string `json:"model"`
	UPSName      string `json:"ups_name"`
	NUTListen    string `json:"nut_listen"`
	Interval     int    `json:"interval"`
	Discovery    *bool  `json:"discovery"`
	DevicePath   string `json:"device_path"`
	DeviceIndex  int    `json:"device_index"`
}

func defaultOptions() addonOptions {
	return addonOptions{
		MQTTHost:   "core-mosquitto",
		MQTTPort:   1883,
		MQTTPrefix: "homeassistant",
		NodeID:     "openups",
		Model:      "auto",
		NUTListen:  ":3493",
		Interval:   5,
	}
}

func (o addonOptions) discoveryEnabled() bool {
	return o.Discovery == nil || *o.Discovery
}

func loadOptions(path string) (addonOptions, error) {
	o := defaultOptions()
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return o, nil
		}
		return o, err
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return o, fmt.Errorf("parse %s: %w", path, err)
	}
	return o, nil
}

// modelList is every registered model plus "auto" (for flags and errors).
func modelList() string {
	names := make([]string, 0, len(core.Registry))
	for _, d := range core.Registry {
		names = append(names, d.Model)
	}
	return strings.Join(names, "|") + "|auto"
}

// autoList is the subset that auto-detection may pick.
func autoList() string {
	names := make([]string, 0, len(core.Registry))
	for _, d := range core.Registry {
		if !d.Untested {
			names = append(names, d.Model)
		}
	}
	return strings.Join(names, ", ")
}

// resolve opens a device. model == "auto" walks the registry in order, skipping
// experimental models, so an OpenUPS is preferred when several are attached.
// path, when set, wins and the pid is read from the usbfs descriptor.
func resolve(ctx context.Context, modelName, path string, idx int) (*core.Device, usb.Dev, error) {
	if path != "" {
		vid, pid, err := usb.Probe(path)
		if err != nil {
			return nil, nil, err
		}
		d, ok := core.DeviceByPID(pid)
		if !ok {
			return nil, nil, fmt.Errorf("%s: unsupported device %04x:%04x", path, vid, pid)
		}
		if modelName != "auto" && d.Model != modelName {
			return nil, nil, fmt.Errorf("%s is a %s, not %s", path, d.Model, modelName)
		}
		dev, err := usb.OpenUSB(ctx, usb.DeviceID{Vendor: vid, Product: pid, MaxIn: d.Size, Path: path}, idx)
		if err != nil {
			return nil, nil, err
		}
		return d, dev, nil
	}

	if modelName != "auto" {
		d, ok := core.DeviceByName(modelName)
		if !ok {
			return nil, nil, fmt.Errorf("unknown model %q (known: %s)", modelName, modelList())
		}
		dev, err := usb.OpenUSB(ctx, usb.DeviceID{Vendor: vendorID, Product: d.PID, MaxIn: d.Size}, idx)
		if err != nil {
			return nil, nil, err
		}
		return d, dev, nil
	}

	for _, d := range core.Registry {
		if d.Untested {
			continue // never guessed: a wrong offset is worse than no device
		}
		dev, err := usb.OpenUSB(ctx, usb.DeviceID{Vendor: vendorID, Product: d.PID, MaxIn: d.Size}, idx)
		if err == nil {
			return d, dev, nil
		}
	}
	return nil, nil, fmt.Errorf("%w: no supported device of %04x (auto-detect covers %s)",
		usb.ErrNotFound, vendorID, autoList())
}

func main() {
	optPath := os.Getenv("OPENUPSD_OPTIONS")
	if optPath == "" {
		optPath = optionsPath
	}
	opts, err := loadOptions(optPath)
	if err != nil {
		log.Fatalf("options: %v", err)
	}

	// Flags default to the add-on options, so they override the file.
	mqttHost := flag.String("mqtt", opts.MQTTHost, "MQTT broker host (empty disables MQTT)")
	mqttPort := flag.Int("mqtt-port", opts.MQTTPort, "MQTT broker port")
	mqttUser := flag.String("mqtt-username", opts.MQTTUsername, "MQTT username")
	mqttPass := flag.String("mqtt-password", opts.MQTTPassword, "MQTT password")
	mqttPrefix := flag.String("mqtt-prefix", opts.MQTTPrefix, "Home Assistant discovery prefix")
	mqttTLS := flag.Bool("mqtt-tls", false, "use TLS for MQTT")
	mqttCA := flag.String("mqtt-ca-certs", "", "CA bundle for MQTT TLS")
	nodeID := flag.String("node-id", opts.NodeID, "node id used in topics and unique ids")
	modelName := flag.String("model", opts.Model, "device model: "+modelList())
	name := flag.String("name", opts.UPSName, "NUT UPS name (empty: derived from the detected model)")
	listen := flag.String("listen", opts.NUTListen, "NUT listen address")
	interval := flag.Duration("interval", time.Duration(opts.Interval)*time.Second, "poll interval")
	devPath := flag.String("dev", opts.DevicePath, "explicit usbfs node, e.g. /dev/bus/usb/003/009")
	idx := flag.Int("n", opts.DeviceIndex, "device index")
	flag.Parse()

	if *modelName != "auto" {
		if _, ok := core.DeviceByName(*modelName); !ok {
			log.Fatalf("unknown model %q (known: %s)", *modelName, modelList())
		}
	}
	if *interval < time.Second {
		*interval = time.Second
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// First open attempt synchronously: the NUT UPS name and the description
	// should follow the detected model instead of a guessed default.
	var (
		model  *core.Device
		dev    usb.Dev
		warned bool
	)
	warnUntested := func(d *core.Device) {
		if d == nil || !d.Untested || warned {
			return
		}
		warned = true
		log.Printf("usb: %s is experimental — byte offsets are unverified on hardware; "+
			"please report the output of `hidprobe -pid %#04x -decode`", d.Model, d.PID)
	}

	if m, d, err := resolve(ctx, *modelName, *devPath, *idx); err != nil {
		log.Printf("usb: %v (retrying)", err)
	} else {
		time.Sleep(openDelay)
		model, dev = m, d
		log.Printf("usb: %s opened", model.Model)
		warnUntested(model)
	}
	defer func() {
		if dev != nil {
			dev.Close()
		}
	}()

	upsName := *name
	if upsName == "" {
		switch {
		case model != nil:
			upsName = model.Model
		default:
			if d, ok := core.DeviceByName(*modelName); ok {
				upsName = d.Model
			} else {
				upsName = "ups"
			}
		}
	}
	desc := "Mini-Box UPS"
	if model != nil {
		desc = model.Desc
	}

	store := core.NewStore(upsName, desc)
	srv := &nut.Server{Addr: *listen, State: store, Version: core.DriverVersion}
	go func() {
		if err := srv.Serve(ctx); err != nil {
			log.Printf("nut: %v", err)
			stop()
		}
	}()

	var bridge *mqtt.Bridge
	if *mqttHost != "" {
		bridge, err = mqtt.New(mqtt.Config{
			Broker:    *mqttHost,
			Port:      *mqttPort,
			Username:  *mqttUser,
			Password:  *mqttPass,
			Prefix:    *mqttPrefix,
			NodeID:    *nodeID,
			Discovery: opts.discoveryEnabled(),
			TLS:       *mqttTLS,
			CACerts:   *mqttCA,
		})
		if err != nil {
			log.Fatalf("mqtt: %v", err)
		}
		defer bridge.Close()
		go connectWithRetry(ctx, bridge, 10*time.Second)
	} else {
		log.Printf("mqtt: disabled")
	}

	failures := 0
	tick := time.NewTicker(*interval)
	defer tick.Stop()
	for {
		if dev == nil {
			if m, d, err := resolve(ctx, *modelName, *devPath, *idx); err != nil {
				log.Printf("usb: %v", err)
			} else {
				time.Sleep(openDelay)
				model, dev, failures = m, d, 0
				log.Printf("usb: %s opened", model.Model)
				warnUntested(model)
				if *name == "" && upsName != model.Model {
					log.Printf("nut: still serving as %q (rename with -name and restart)", upsName)
				}
			}
		}
		if dev != nil {
			r, err := model.Poll(dev, model.Size)
			if err != nil {
				failures++
				log.Printf("usb: poll failed (%d/%d): %v", failures, maxFailures, err)
				if failures >= maxFailures {
					dev.Close()
					dev, failures = nil, 0
					store.MarkStale()
				}
			} else {
				failures = 0
				serial := dev.Serial()
				store.Update(model.NUTVars(r, serial))
				if bridge != nil {
					bridge.Publish(model, r, serial)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func connectWithRetry(ctx context.Context, b *mqtt.Bridge, every time.Duration) {
	for {
		if err := b.Connect(); err == nil {
			log.Printf("mqtt: connected")
			return
		} else {
			log.Printf("mqtt: connect: %v (retry in %s)", err, every)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
