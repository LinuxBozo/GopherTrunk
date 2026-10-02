package alerts

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/config"
)

// mqttChannel is a minimal MQTT 3.1.1 publisher: CONNECT with optional
// user/password, PUBLISH at QoS 0, PINGREQ keep-alive, reconnect on error.
// It is deliberately small — one goroutine-free, lock-protected connection
// that QoS-0 publishes JSON — rather than a dependency: the trunk-recorder
// MQTT plugin's consumers (Home Assistant, Node-RED, a dashboard) need
// exactly that, and a broker that is down must never stall the event loop.
type mqttChannel struct {
	name    string
	cfg     config.AlertChannelConfig
	addr    string
	useTLS  bool
	topic   string
	timeout time.Duration

	mu       sync.Mutex
	conn     net.Conn
	lastIO   time.Time
	clientID string
}

func newMQTTChannel(cfg config.AlertChannelConfig) (Channel, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil {
		return nil, fmt.Errorf("alerts: mqtt url: %w", err)
	}
	useTLS := false
	switch u.Scheme {
	case "tcp", "mqtt":
	case "ssl", "tls", "mqtts":
		useTLS = true
	default:
		return nil, fmt.Errorf("alerts: mqtt url scheme %q (want tcp:// or ssl://)", u.Scheme)
	}
	addr := u.Host
	if u.Port() == "" {
		if useTLS {
			addr += ":8883"
		} else {
			addr += ":1883"
		}
	}
	topic := strings.Trim(cfg.Topic, "/")
	if topic == "" {
		topic = "gophertrunk"
	}
	return &mqttChannel{
		name: cfg.Name, cfg: cfg, addr: addr, useTLS: useTLS, topic: topic,
		timeout:  cfg.TimeoutDuration(),
		clientID: fmt.Sprintf("gophertrunk-%d", time.Now().UnixNano()%1_000_000),
	}, nil
}

func (c *mqttChannel) Name() string { return c.name }
func (c *mqttChannel) Type() string { return "mqtt" }

// Send publishes the alert JSON to <topic>/alerts/<rule>.
func (c *mqttChannel) Send(ctx context.Context, n Notification) error {
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return c.Publish(ctx, c.topic+"/alerts/"+sanitizeTopic(n.Rule), b)
}

// PublishEvent mirrors one bus event to <topic>/events/<kind>.
func (c *mqttChannel) PublishEvent(ctx context.Context, kind string, payload []byte) error {
	return c.Publish(ctx, c.topic+"/events/"+sanitizeTopic(kind), payload)
}

// MirrorsEvents reports whether the channel asked for every bus event.
func (c *mqttChannel) MirrorsEvents() bool { return c.cfg.MirrorEvents }

// Publish sends one QoS-0 PUBLISH, (re)connecting as needed. One failed
// attempt reconnects once.
func (c *mqttChannel) Publish(ctx context.Context, topic string, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if err := c.ensureConnLocked(ctx); err != nil {
			return err
		}
		if err := c.publishLocked(topic, payload); err != nil {
			c.dropLocked()
			if attempt == 1 {
				return err
			}
			continue
		}
		return nil
	}
	return errors.New("mqtt: publish failed")
}

// Close shuts the connection with a DISCONNECT.
func (c *mqttChannel) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = c.conn.Write([]byte{0xE0, 0x00}) // DISCONNECT
	}
	c.dropLocked()
	return nil
}

func (c *mqttChannel) dropLocked() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

func (c *mqttChannel) ensureConnLocked(ctx context.Context) error {
	if c.conn != nil {
		// Keep-alive: ping if idle for a while so the broker keeps us.
		if time.Since(c.lastIO) > 45*time.Second {
			if err := c.pingLocked(); err != nil {
				c.dropLocked()
			} else {
				return nil
			}
		} else {
			return nil
		}
	}
	d := net.Dialer{Timeout: c.timeout}
	var (
		conn net.Conn
		err  error
	)
	if c.useTLS {
		conn, err = tls.DialWithDialer(&d, "tcp", c.addr, &tls.Config{ServerName: hostOnly(c.addr)})
	} else {
		conn, err = d.DialContext(ctx, "tcp", c.addr)
	}
	if err != nil {
		return fmt.Errorf("mqtt: connect %s: %w", c.addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(c.timeout))
	if _, err := conn.Write(mqttConnectPacket(c.clientID, c.cfg.User, c.cfg.Password, 60)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("mqtt: CONNECT: %w", err)
	}
	// CONNACK: fixed header 0x20 0x02, flags, return code.
	var ack [4]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		_ = conn.Close()
		return fmt.Errorf("mqtt: CONNACK: %w", err)
	}
	if ack[0] != 0x20 || ack[1] != 0x02 {
		_ = conn.Close()
		return fmt.Errorf("mqtt: unexpected CONNACK %x", ack)
	}
	if ack[3] != 0 {
		_ = conn.Close()
		return fmt.Errorf("mqtt: broker refused connection (return code %d: %s)", ack[3], mqttConnackReason(ack[3]))
	}
	_ = conn.SetDeadline(time.Time{})
	c.conn, c.lastIO = conn, time.Now()
	return nil
}

func (c *mqttChannel) publishLocked(topic string, payload []byte) error {
	_ = c.conn.SetWriteDeadline(time.Now().Add(c.timeout))
	_, err := c.conn.Write(mqttPublishPacket(topic, payload))
	if err == nil {
		c.lastIO = time.Now()
	}
	return err
}

func (c *mqttChannel) pingLocked() error {
	_ = c.conn.SetDeadline(time.Now().Add(c.timeout))
	defer c.conn.SetDeadline(time.Time{})
	if _, err := c.conn.Write([]byte{0xC0, 0x00}); err != nil {
		return err
	}
	var resp [2]byte
	if _, err := io.ReadFull(c.conn, resp[:]); err != nil {
		return err
	}
	if resp[0] != 0xD0 {
		return fmt.Errorf("mqtt: unexpected PINGRESP %x", resp)
	}
	c.lastIO = time.Now()
	return nil
}

func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// sanitizeTopic makes a rule / kind name safe as one topic level.
func sanitizeTopic(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "_"
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '/', '+', '#', ' ':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// --- packet encoders (MQTT 3.1.1, OASIS spec §3) ---------------------------

func mqttEncodeRemaining(n int) []byte {
	var out []byte
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			return out
		}
	}
}

func mqttString(s string) []byte {
	out := make([]byte, 2+len(s))
	binary.BigEndian.PutUint16(out, uint16(len(s)))
	copy(out[2:], s)
	return out
}

// mqttConnectPacket encodes CONNECT: protocol name "MQTT", level 4, clean
// session, optional user name / password, keep-alive seconds.
func mqttConnectPacket(clientID, user, pass string, keepAlive uint16) []byte {
	var body []byte
	body = append(body, mqttString("MQTT")...)
	body = append(body, 0x04)
	flags := byte(0x02) // clean session
	if user != "" {
		flags |= 0x80
		if pass != "" {
			flags |= 0x40
		}
	}
	body = append(body, flags)
	var ka [2]byte
	binary.BigEndian.PutUint16(ka[:], keepAlive)
	body = append(body, ka[:]...)
	body = append(body, mqttString(clientID)...)
	if user != "" {
		body = append(body, mqttString(user)...)
		if pass != "" {
			body = append(body, mqttString(pass)...)
		}
	}
	pkt := []byte{0x10}
	pkt = append(pkt, mqttEncodeRemaining(len(body))...)
	return append(pkt, body...)
}

// mqttPublishPacket encodes a QoS-0, non-retained PUBLISH.
func mqttPublishPacket(topic string, payload []byte) []byte {
	body := append(mqttString(topic), payload...)
	pkt := []byte{0x30}
	pkt = append(pkt, mqttEncodeRemaining(len(body))...)
	return append(pkt, body...)
}

func mqttConnackReason(code byte) string {
	switch code {
	case 1:
		return "unacceptable protocol version"
	case 2:
		return "identifier rejected"
	case 3:
		return "server unavailable"
	case 4:
		return "bad user name or password"
	case 5:
		return "not authorized"
	}
	return "unknown"
}
