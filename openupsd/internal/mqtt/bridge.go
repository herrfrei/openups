// Package mqtt publishes UPS status to MQTT with Home Assistant discovery.
package mqtt

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/herrfrei/openupsd/internal/core"
)

const (
	statusExpire = 60 // seconds; HA marks a sensor unknown if no update arrives
	publishQoS   = 1
	publishWait  = 5 * time.Second
	connectWait  = 10 * time.Second
)

// Config describes the broker connection and the topic layout.
type Config struct {
	Broker    string
	Port      int
	Username  string
	Password  string
	ClientID  string
	Prefix    string // discovery prefix, e.g. "homeassistant"
	NodeID    string
	Base      string // state topic root, e.g. "openups/<node>"
	Discovery bool
	TLS       bool
	CACerts   string
}

func (c *Config) applyDefaults() {
	if c.Port == 0 {
		c.Port = 1883
	}
	if c.Prefix == "" {
		c.Prefix = "homeassistant"
	}
	if c.NodeID == "" {
		c.NodeID = "openups"
	}
	if c.Base == "" {
		c.Base = "openups/" + c.NodeID
	}
	if c.ClientID == "" {
		c.ClientID = "openupsd-" + c.NodeID
	}
}

// Bridge publishes discovery and status. Safe for concurrent use.
type Bridge struct {
	cfg   Config
	c     paho.Client
	avail string

	mu            sync.Mutex
	discoverySent bool
	deviceKey     string
}

// New builds the client. It does not connect.
func New(cfg Config) (*Bridge, error) {
	cfg.applyDefaults()
	b := &Bridge{cfg: cfg, avail: cfg.Base + "/availability"}

	scheme := "tcp"
	if cfg.TLS {
		scheme = "ssl"
	}
	opts := paho.NewClientOptions().
		AddBroker(fmt.Sprintf("%s://%s:%d", scheme, cfg.Broker, cfg.Port)).
		SetClientID(cfg.ClientID).
		SetKeepAlive(60*time.Second).
		SetPingTimeout(10*time.Second).
		SetAutoReconnect(true).
		SetConnectRetry(false). // retried explicitly by the caller
		SetMaxReconnectInterval(30*time.Second).
		SetCleanSession(true).
		// The will must be registered before connecting: it is what marks the
		// entities unavailable if the process dies without a clean disconnect.
		SetWill(b.avail, "offline", publishQoS, true)

	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
		opts.SetPassword(cfg.Password)
	}
	if cfg.TLS {
		tc := &tls.Config{MinVersion: tls.VersionTLS12}
		if cfg.CACerts != "" {
			pem, err := os.ReadFile(cfg.CACerts)
			if err != nil {
				return nil, fmt.Errorf("mqtt: read CA file: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("mqtt: no certificates in %s", cfg.CACerts)
			}
			tc.RootCAs = pool
		}
		opts.SetTLSConfig(tc)
	}

	opts.SetOnConnectHandler(b.onConnect)
	opts.SetConnectionLostHandler(func(_ paho.Client, err error) {
		log.Printf("mqtt: connection lost: %v", err)
	})

	b.c = paho.NewClient(opts)
	return b, nil
}

// Connect waits for a single connection attempt.
func (b *Bridge) Connect() error {
	tok := b.c.Connect()
	if !tok.WaitTimeout(connectWait) {
		return fmt.Errorf("mqtt: connect to %s:%d timed out", b.cfg.Broker, b.cfg.Port)
	}
	return tok.Error()
}

// onConnect runs on every (re)connect. It must not block: some paho versions
// call it from the connection goroutine, so the republish happens on the next
// Publish call from the poll loop.
func (b *Bridge) onConnect(paho.Client) {
	log.Printf("mqtt: connected to %s:%d", b.cfg.Broker, b.cfg.Port)
	b.mu.Lock()
	b.discoverySent = false
	b.mu.Unlock()
	b.c.Publish(b.avail, publishQoS, true, "online")
}

// Publish sends discovery when needed and then every status topic.
func (b *Bridge) Publish(d *core.Device, r core.Reading, serial string) {
	key := fmt.Sprintf("%s|%s|%s", d.Model, serial, r.Firmware)

	b.mu.Lock()
	needDiscovery := b.cfg.Discovery && (!b.discoverySent || b.deviceKey != key)
	b.mu.Unlock()

	if needDiscovery {
		b.publishDiscovery(d, r, serial)
		b.mu.Lock()
		b.discoverySent, b.deviceKey = true, key
		b.mu.Unlock()
	}
	for _, s := range d.Sensors {
		if v := r.Value(s); v != "" {
			b.publish(b.stateTopic(s), v, true)
		}
	}
}

// Close publishes the offline marker and disconnects.
func (b *Bridge) Close() {
	if b.c == nil {
		return
	}
	if b.c.IsConnected() {
		b.c.Publish(b.avail, publishQoS, true, "offline").WaitTimeout(2 * time.Second)
	}
	b.c.Disconnect(250)
}

func (b *Bridge) publishDiscovery(d *core.Device, r core.Reading, serial string) {
	dev := b.devicePayload(d, r, serial)
	for _, s := range d.Sensors {
		payload, err := json.Marshal(b.discoveryPayload(s, dev))
		if err != nil {
			log.Printf("mqtt: marshal discovery for %s: %v", s.Slug, err)
			continue
		}
		b.publish(b.discoveryTopic(s), string(payload), true)
	}
}

func (b *Bridge) publish(topic, payload string, retain bool) {
	if !b.c.IsConnected() {
		return // the poll loop retries on the next tick
	}
	tok := b.c.Publish(topic, publishQoS, retain, payload)
	if !tok.WaitTimeout(publishWait) {
		log.Printf("mqtt: publish %s timed out", topic)
		return
	}
	if err := tok.Error(); err != nil {
		log.Printf("mqtt: publish %s: %v", topic, err)
	}
}

func (b *Bridge) stateTopic(s core.Sensor) string {
	return fmt.Sprintf("%s/sensor/%s/state", b.cfg.Base, s.Slug)
}

func (b *Bridge) discoveryTopic(s core.Sensor) string {
	return fmt.Sprintf("%s/sensor/%s/%s/config", b.cfg.Prefix, b.cfg.NodeID, s.Slug)
}

func (b *Bridge) discoveryPayload(s core.Sensor, device map[string]any) map[string]any {
	p := map[string]any{
		"name":                  s.Name,
		"unique_id":             b.cfg.NodeID + "_" + s.Slug,
		"state_topic":           b.stateTopic(s),
		"availability_topic":    b.avail,
		"payload_available":     "online",
		"payload_not_available": "offline",
		"device":                device,
		"expire_after":          statusExpire,
	}
	if s.Unit != "" {
		p["unit_of_measurement"] = s.Unit
	}
	if s.Class != "" {
		p["device_class"] = s.Class
	}
	if s.StateClass != "" {
		p["state_class"] = s.StateClass
	}
	if s.Icon != "" {
		p["icon"] = s.Icon
	}
	if s.Category != "" {
		p["entity_category"] = s.Category
	}
	// Never set this on text entities: HA would try to round a string.
	if !s.Text && s.Precision > 0 {
		p["suggested_display_precision"] = s.Precision
	}
	return p
}

func (b *Bridge) devicePayload(d *core.Device, r core.Reading, serial string) map[string]any {
	name := d.ModelName
	if serial != "" {
		name += " (" + serial + ")"
	}
	m := map[string]any{
		"identifiers":  []string{b.cfg.NodeID},
		"name":         name,
		"manufacturer": d.Mfr,
		"model":        d.ModelName,
	}
	if r.Firmware != "" {
		m["sw_version"] = r.Firmware
	}
	if serial != "" {
		m["serial_number"] = serial
	}
	return m
}
