package config

import "testing"

// A ZERO FROM HERE MEANS "DO NOT KNOW", NEVER "PORT ZERO".
//
// The settings API refuses an ingest listener that asked for this port. If an
// unreadable addr came back as a real port number, the refusal would fire on
// whatever that number happened to be and lock an operator out of their own
// settings page over a string this function merely failed to parse.
//
// Mutation: drop the `n < 1 || n > 65535` clause. Observed to fail with
//
//	addr "" -> 0, want 0
//
// on the "no port at all" case once Atoi's zero value reaches the caller.
func TestListenPortNumberIsZeroWhenTheAddrNamesNoUsablePort(t *testing.T) {
	cases := []struct {
		addr string
		want int
	}{
		// THE CONTROL. A function that answered 0 for everything would satisfy
		// every case below and silently reserve nothing on every install.
		{":8099", 8099},
		{"0.0.0.0:443", 443},
		{"127.0.0.1:1935", 1935},
		{"[::]:8080", 8080},
		{"", 0},
		{"8099", 0},
		{":", 0},
		{":http", 0},
		{":0", 0},
		{":70000", 0},
		{":-1", 0},
	}
	for _, tc := range cases {
		if got := (Config{Addr: tc.addr}).ListenPortNumber(); got != tc.want {
			t.Errorf("addr %q -> %d, want %d", tc.addr, got, tc.want)
		}
	}
}
