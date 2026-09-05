package relay

import (
	"fmt"
	"net"
	"testing"
)

// requireIPv6 skips on hosts where IPv6 is compiled out or disabled, which is
// the case in some minimal containers.
func requireIPv6(t *testing.T) {
	t.Helper()
	c, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback})
	if err != nil {
		t.Skipf("no IPv6 loopback on this host: %v", err)
	}
	_ = c.Close()
}

func TestNewBindsTheRequestedFamily(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
		ipv6 bool
		// wantBound is only pinned for concrete addresses: a wildcard bind
		// resolves to whichever family the host can serve both over.
		wantBound string
		wantHost  string
	}{
		{
			name:      "the default is IPv4 loopback",
			wantBound: "127.0.0.1",
			wantHost:  "127.0.0.1",
		},
		{
			name:      "an IPv6 address binds IPv6",
			opts:      []Option{WithListenIP(net.IPv6loopback)},
			ipv6:      true,
			wantBound: "::1",
			wantHost:  "[::1]",
		},
		{
			name:     "the IPv4 wildcard advertises IPv4 loopback",
			opts:     []Option{WithListenIP(net.IPv4zero)},
			wantHost: "127.0.0.1",
		},
		{
			name:     "the IPv6 wildcard advertises IPv6 loopback",
			opts:     []Option{WithListenIP(net.IPv6unspecified)},
			ipv6:     true,
			wantHost: "[::1]",
		},
		{
			name:     "an explicit advertise address wins over the bound one",
			opts:     []Option{WithListenIP(net.IPv4zero), WithAdvertiseIP(net.IPv4(10, 0, 0, 7))},
			wantHost: "10.0.0.7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.ipv6 {
				requireIPv6(t)
			}
			h, err := New(testLogger(), 0, tt.opts...)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer h.Close()

			if got := h.conn.LocalAddr().(*net.UDPAddr).IP.String(); tt.wantBound != "" && got != tt.wantBound {
				t.Errorf("bound %s, want %s", got, tt.wantBound)
			}
			if want := fmt.Sprintf("udp://%s:%d", tt.wantHost, h.Port()); h.InputURL() != want {
				t.Errorf("InputURL() = %q, want %q", h.InputURL(), want)
			}
			got := mustSubscribe(t, h, "sub", 9999)
			if want := fmt.Sprintf("udp://%s:9999", tt.wantHost); got != want {
				t.Errorf("Subscribe() = %q, want %q", got, want)
			}
		})
	}
}

func TestSubscribeAddrTargetsAnArbitraryHost(t *testing.T) {
	h := newTestHub(t)

	if got, want := mustSubscribeAddr(t, h, "remote", net.IPv4(192, 168, 1, 20), 5000), "udp://192.168.1.20:5000"; got != want {
		t.Errorf("SubscribeAddr() = %q, want %q", got, want)
	}
	if got, want := mustSubscribeAddr(t, h, "remote6", net.ParseIP("2001:db8::1"), 5000), "udp://[2001:db8::1]:5000"; got != want {
		t.Errorf("SubscribeAddr() = %q, want %q", got, want)
	}
}

// The point of the wildcard bind is one hub serving both families at once, so
// an IPv4 publisher must still reach an IPv6 subscriber and vice versa.
func TestWildcardHubFansOutAcrossBothFamilies(t *testing.T) {
	requireIPv6(t)
	h, err := New(testLogger(), 0, WithListenIP(net.IPv6unspecified))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	sub4, port4 := boundSubscriber(t)
	sub6, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback})
	if err != nil {
		t.Fatalf("bind IPv6 subscriber: %v", err)
	}
	t.Cleanup(func() { _ = sub6.Close() })

	mustSubscribeAddr(t, h, "v4", net.IPv4(127, 0, 0, 1), port4)
	mustSubscribeAddr(t, h, "v6", net.IPv6loopback, sub6.LocalAddr().(*net.UDPAddr).Port)

	payload := []byte("dual stack")
	publish(t, h, payload, 3) // publish dials IPv4 loopback
	waitForRx(t, h, 1)

	assertDelivered(t, "v4", sub4, payload, 3)
	assertDelivered(t, "v6", sub6, payload, 3)
}
