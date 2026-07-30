package mqtt

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

// Publisher is the MQTT side, narrowed the same way alerts.Doer narrows HTTP.
//
// It exists so the topic tree, the payload shapes and the retain flag can all
// be asserted at wire level in a test with no broker running, which is what
// makes the acceptance suite a confirmation rather than the only coverage.
type Publisher interface {
	// Publish sends one message. It returns an error rather than buffering when
	// the connection is down -- see Client.Publish for why that is correct here.
	Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error
	// Connected reports whether the link to the broker is currently up.
	Connected() bool
}

// QoS is 1 on every publish this package makes, never 0.
//
// MQTT permits a broker to decline to store a retained QoS 0 message. Retained
// state that must survive a broker restart -- which is the entire point of this
// feature -- therefore has to be QoS 1. The cost is one PUBACK round trip per
// message, on a tick measured in seconds.
const QoS byte = 1

// Config is what an operator sets.
type Config struct {
	// BrokerURL is a single URL: mqtt://, mqtts://, ws:// or wss://.
	BrokerURL string
	Username  string
	// Password comes from the sealed settings blob, never from the URL. A
	// password in a URL ends up in logs, in `ps` output and in error strings.
	Password string
	// ClientID must be unique on the broker. A collision is the number-one
	// cause of a reconnect loop nobody can explain: the broker disconnects the
	// older session on every connect, and both clients reconnect forever.
	ClientID string
	// Prefix and Instance build the topic tree.
	Prefix   string
	Instance string
	// KeepAliveSec bounds how long a dead link goes unnoticed. Without it a
	// half-open TCP connection looks healthy indefinitely and the will message
	// -- the whole availability story -- never fires.
	KeepAliveSec uint16
	// TLSSkipVerify is for a broker with a self-signed certificate. Off by
	// default and surfaced in the UI as what it is.
	TLSSkipVerify bool
}

// Defaults applied to a zero-valued Config.
const (
	DefaultKeepAliveSec  = 30
	defaultConnectTimeo  = 10 * time.Second
	defaultPublishTimeo  = 5 * time.Second
	reconnectBackoffMin  = time.Second
	reconnectBackoffMax  = 30 * time.Second
	reconnectBackoffMult = 2
)

// Client is a live connection to a broker.
type Client struct {
	cm     *autopaho.ConnectionManager
	topics *Topics
	log    *slog.Logger

	up atomic.Bool

	closeOnce sync.Once
}

// backoff is exponential from 1s to 30s, mirroring the existing alerts
// notifier. A broker that is down is usually down for a while, and a client
// retrying every second is a client that shows up in somebody's logs as an
// attack.
func backoff(attempt int) time.Duration {
	d := reconnectBackoffMin
	for range attempt {
		d *= reconnectBackoffMult
		if d >= reconnectBackoffMax {
			return reconnectBackoffMax
		}
	}
	return d
}

// Connect opens the connection and begins maintaining it in the background.
//
// It returns as soon as the configuration is valid; it does NOT wait for the
// broker. A telemetry publisher that refused to start because the broker was
// rebooting would take the rest of polyemesis with it, and the whole design
// assumes the link comes and goes.
func Connect(ctx context.Context, cfg Config, log *slog.Logger) (*Client, error) {
	topics, err := NewTopics(cfg.Prefix, cfg.Instance)
	if err != nil {
		return nil, err
	}
	u, err := parseBroker(cfg.BrokerURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
		return nil, errors.New("mqtt client id is empty; two polyemesis instances sharing one would disconnect each other in a loop")
	}
	keepAlive := cfg.KeepAliveSec
	if keepAlive == 0 {
		keepAlive = DefaultKeepAliveSec
	}

	c := &Client{topics: topics, log: log}

	pc := autopaho.ClientConfig{
		ServerUrls:       []*url.URL{u},
		KeepAlive:        keepAlive,
		ConnectTimeout:   defaultConnectTimeo,
		ReconnectBackoff: backoff,
		ConnectUsername:  cfg.Username,
		ConnectPassword:  []byte(cfg.Password),
		// The availability story in one field. Retained and QoS 1 so a
		// subscriber that connects after polyemesis died still learns it is
		// gone -- which is the case this whole package exists for.
		WillMessage: &paho.WillMessage{
			Topic:   topics.Status(),
			Payload: []byte(Offline),
			QoS:     QoS,
			Retain:  true,
		},
		// Queue is deliberately left nil.
		//
		// autopaho substitutes memory.New() for a nil queue
		// (autopaho/auto.go:271 in v0.23.0), and its own field comment claims a
		// nil queue makes Publish return an error -- the comment is wrong about
		// the substitution. But the substitution is INERT here: the queue is
		// read only by PublishViaQueue (auto.go:488), and this package never
		// calls it. ConnectionManager.Publish (auto.go:460) bypasses the queue
		// entirely and returns ConnectionDownError when the link is down.
		//
		// That is exactly the behaviour retained telemetry wants. A 90-second-
		// old bitrate replayed on reconnect is worse than no reading at all,
		// because the next tick republishes ground truth anyway. So: use
		// Publish, never PublishViaQueue, and no custom no-op queue is needed.
		// TestClientNeverUsesTheQueuePath holds that line.
		OnConnectionUp: func(*autopaho.ConnectionManager, *paho.Connack) {
			c.up.Store(true)
			log.Info("mqtt connected", "broker", redactURL(u), "instance", topics.Instance())
		},
		OnConnectionDown: func() bool {
			c.up.Store(false)
			log.Warn("mqtt connection lost; retrying", "broker", redactURL(u))
			return true
		},
		OnConnectError: func(err error) {
			log.Warn("mqtt connect failed", "broker", redactURL(u), "err", err)
		},
		ClientConfig: paho.ClientConfig{ClientID: cfg.ClientID},
	}
	if u.Scheme == "mqtts" || u.Scheme == "ssl" || u.Scheme == "wss" {
		pc.TlsCfg = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: cfg.TLSSkipVerify, //nolint:gosec // operator opt-in for a self-signed broker; surfaced in the UI as exactly that
		}
	}

	cm, err := autopaho.NewConnection(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("mqtt: %w", err)
	}
	c.cm = cm
	return c, nil
}

// Topics is the topic tree this client publishes to.
func (c *Client) Topics() *Topics { return c.topics }

// Connected reports whether the link is up.
func (c *Client) Connected() bool { return c.up.Load() }

// Publish sends one message, waiting for the QoS 1 acknowledgement.
//
// It does not buffer. When the broker is unreachable this returns
// autopaho.ConnectionDownError and the caller drops the reading, which is
// correct for state that is republished on the next tick and would be a lie if
// it were replayed later.
func (c *Client) Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error {
	if err := Valid(topic); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, defaultPublishTimeo)
	defer cancel()
	_, err := c.cm.Publish(ctx, &paho.Publish{
		Topic:   topic,
		QoS:     qos,
		Retain:  retain,
		Payload: payload,
	})
	return err
}

// Close publishes a clean `offline` and disconnects.
//
// The explicit offline matters: the will message only fires when the broker
// notices the connection died. On a clean shutdown the broker sees a proper
// DISCONNECT and, per the specification, discards the will -- so without this
// a deliberately stopped polyemesis would sit on the broker reading `online`
// forever.
func (c *Client) Close(ctx context.Context) error {
	var err error
	c.closeOnce.Do(func() {
		if c.Connected() {
			if perr := c.Publish(ctx, c.topics.Status(), QoS, true, []byte(Offline)); perr != nil {
				c.log.Warn("could not publish a clean offline; the will message will cover it once the broker times the connection out", "err", perr)
			}
		}
		err = c.cm.Disconnect(ctx)
		c.up.Store(false)
	})
	return err
}

// parseBroker accepts the four schemes autopaho supports and refuses anything
// else by name rather than failing later inside the dialler.
func parseBroker(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("mqtt broker URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("mqtt broker URL is unparseable: %w", err)
	}
	switch u.Scheme {
	case "mqtt", "tcp", "mqtts", "ssl", "ws", "wss":
	default:
		return nil, fmt.Errorf("mqtt broker scheme %q is not one of mqtt, mqtts, tcp, ssl, ws or wss", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("mqtt broker URL %q has no host", raw)
	}
	if u.User != nil {
		// Refused rather than accepted-and-moved, because an operator who put
		// a password in the URL needs to know it would have been logged.
		return nil, errors.New("mqtt broker URL carries credentials; put the username and password in their own fields so the password is sealed and never logged")
	}
	return u, nil
}

// redactURL is what reaches a log line. The scheme and host are the useful
// part; anything else on the URL is not worth the risk.
func redactURL(u *url.URL) string {
	return u.Scheme + "://" + u.Host
}
