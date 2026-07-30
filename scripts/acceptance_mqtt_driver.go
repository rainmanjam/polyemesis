//go:build ignore

// Driver for acceptance-mqtt.sh.
//
// Two halves. The `configure` and `dest` commands drive polyemesis through the
// same REST API the UI uses. The `dump` command is an independent MQTT
// subscriber that connects to the broker AFTER the fact and reports what it
// receives.
//
// That ordering is the whole suite. Retained delivery is defined by what a
// subscriber that was not connected when the state changed still gets, so a
// driver that subscribed first and watched messages arrive would prove ordinary
// pub/sub and say nothing at all about `retain`.
//
//	go run scripts/acceptance_mqtt_driver.go <cmd> [args]
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

const (
	user = "admin"
	pass = "acceptance-pw-1"
)

var (
	base string
	jar  []*http.Cookie
	csrf string
)

// die writes to stderr, never stdout. stdout is a value channel here -- the
// shell captures it -- and an error printed onto it becomes a value the suite
// silently treats as a measurement.
func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "driver: "+format+"\n", a...)
	os.Exit(1)
}

// The API authenticates with a session cookie and a CSRF header, not with
// basic auth. Getting that wrong produces a 401 on every call, which reads
// like a broken driver rather than a broken assumption.
func api(method, path string, body, out any) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			die("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		die("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, c := range jar {
		req.AddCookie(c)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		die("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if cs := resp.Cookies(); len(cs) > 0 {
		jar = append(jar, cs...)
		for _, c := range cs {
			if c.Name == "polyemesis_csrf" {
				csrf = c.Value
			}
		}
	}
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		die("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			die("decode %s: %v: %s", path, err, string(raw))
		}
	}
}

func waitUp() {
	for range 80 {
		resp, err := http.Get(base + "/health")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	die("server never became reachable at %s", base)
}

// setup creates the first account. Idempotent from the suite's point of view:
// a second call fails, and the caller ignores it.
func setup() {
	waitUp()
	api(http.MethodPost, "/setup", map[string]string{"username": user, "password": pass}, nil)
	fmt.Println("SETUP_OK")
}

func login() {
	waitUp()
	api(http.MethodPost, "/auth/login", map[string]string{"username": user, "password": pass}, nil)
}

// configure enables MQTT with a 1s interval so the suite does not spend its
// wall-clock budget waiting for ticks.
func configure(broker, instance string) {
	var settings map[string]any
	api(http.MethodGet, "/settings", nil, &settings)

	settings["mqtt"] = map[string]any{
		"enabled":          true,
		"brokerUrl":        broker,
		"prefix":           "polyemesis",
		"instance":         instance,
		"intervalSeconds":  1,
		"keepAliveSeconds": 5,
		"discovery":        true,
	}
	api(http.MethodPut, "/settings", settings, nil)
	fmt.Println("ok")
}

// dest creates a destination that will never connect. It does not need to: the
// suite is testing that its STATE is published, and a destination pointing at a
// black hole has a state.
func dest(name string) {
	var out map[string]any
	api(http.MethodPost, "/destinations", map[string]any{
		"name":         name,
		"kind":         "rtmp",
		"url":          "rtmp://127.0.0.1:1/live",
		"streamKey":    "acceptance-stream-key-do-not-publish",
		"audioBitrate": 160,
		"enabled":      false,
	}, &out)
	fmt.Println("ok")
}

// rmfirst deletes the first destination in the list and prints its name, so the
// suite can name the topic that must go away.
func rmfirst() {
	var list []struct {
		Destination struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"destination"`
	}
	// The list endpoint wraps each row: `[{"destination":{...},"routing":{...}}]`
	// while create returns `{"destination":{...}}`. Getting this wrong is a
	// silent empty result rather than an error, so it is spelled out.
	api(http.MethodGet, "/destinations", nil, &list)
	if len(list) == 0 {
		die("no destinations to delete")
	}
	d := list[0].Destination
	api(http.MethodDelete, fmt.Sprintf("/destinations/%d", d.ID), nil, nil)
	fmt.Println(d.Name)
}

// dump connects a FRESH subscriber, collects everything retained under the
// prefix, and prints one `topic<TAB>payload` line per topic, sorted.
//
// The wait is a fixed window rather than a message count: the point is to
// report what a new subscriber receives, and asserting a count here would move
// the suite's judgement into the driver.
func dump(broker, prefix string, wait time.Duration) {
	u, err := url.Parse(broker)
	if err != nil {
		die("broker URL: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait+15*time.Second)
	defer cancel()

	// got is written from paho's delivery goroutine and read from this one, so
	// it needs a lock. The sleep below is not synchronisation: it bounds how
	// long we collect, and nothing in it establishes a happens-before edge.
	var mu sync.Mutex
	got := map[string]string{}
	ready := make(chan struct{})
	var once bool

	cm, err := autopaho.NewConnection(ctx, autopaho.ClientConfig{
		ServerUrls:     []*url.URL{u},
		KeepAlive:      10,
		ConnectTimeout: 10 * time.Second,
		// A fresh session every time. Without this the broker could replay a
		// previous run's queued messages and the suite would be reading its own
		// history rather than the current retained set.
		CleanStartOnInitialConnection: true,
		OnConnectionUp: func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
			if _, err := cm.Subscribe(ctx, &paho.Subscribe{
				Subscriptions: []paho.SubscribeOptions{
					{Topic: prefix + "/#", QoS: 1},
					{Topic: "homeassistant/#", QoS: 1},
				},
			}); err != nil {
				die("subscribe: %v", err)
			}
			if !once {
				once = true
				close(ready)
			}
		},
		ClientConfig: paho.ClientConfig{
			ClientID: fmt.Sprintf("acceptance-dump-%d", time.Now().UnixNano()),
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(pr paho.PublishReceived) (bool, error) {
					mu.Lock()
					got[pr.Packet.Topic] = string(pr.Packet.Payload)
					mu.Unlock()
					return true, nil
				},
			},
		},
	})
	if err != nil {
		die("connect: %v", err)
	}
	select {
	case <-ready:
	case <-ctx.Done():
		die("never connected to the broker at %s", broker)
	}

	time.Sleep(wait)
	_ = cm.Disconnect(context.Background())

	mu.Lock()
	defer mu.Unlock()
	topics := make([]string, 0, len(got))
	for topic := range got {
		topics = append(topics, topic)
	}
	sort.Strings(topics)
	for _, topic := range topics {
		// Payloads are single-line JSON or a bare word, so a tab separator is
		// unambiguous. A zero-length payload -- which is how a retained message
		// is deleted -- prints as an empty field rather than vanishing.
		fmt.Printf("%s\t%s\n", topic, strings.ReplaceAll(got[topic], "\n", " "))
	}
}

func main() {
	if len(os.Args) < 3 {
		die("usage: driver <baseURL> <cmd> [args]")
	}
	base = strings.TrimSuffix(os.Args[1], "/") + "/api/v1"
	cmd, args := os.Args[2], os.Args[3:]

	// dump talks to the broker, not to polyemesis, so it must not try to log in
	// -- and must still work after the server has been killed, which is exactly
	// when the will-message check runs.
	if cmd == "dump" {
		if len(args) != 2 {
			die("usage: driver <baseURL> dump <brokerURL> <prefix>")
		}
		dump(args[0], args[1], 3*time.Second)
		return
	}
	if cmd == "setup" {
		setup()
		return
	}

	login()
	switch cmd {
	case "configure":
		if len(args) != 2 {
			die("usage: driver <baseURL> configure <brokerURL> <instance>")
		}
		configure(args[0], args[1])
	case "dest":
		if len(args) != 1 {
			die("usage: driver <baseURL> dest <name>")
		}
		dest(args[0])
	case "rmfirst":
		rmfirst()
	default:
		die("unknown command %q", cmd)
	}
}
