package rtmpserver

import (
	"bytes"
	"net"
	"testing"

	"github.com/bluenviron/gortmplib"
)

// rawConn hands back the transport under an RTMP connection when it is a real
// net.Conn, so the server can set deadlines on it, and nil otherwise. Returning
// a non-nil wrapper for a buffer would make a deadline call panic at runtime.

func TestRawConnReturnsTheUnderlyingNetConn(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()

	if got := rawConn(&gortmplib.ServerConn{RW: left}); got != left {
		t.Errorf("rawConn over a net.Conn = %v; want the same conn back", got)
	}
}

func TestRawConnIsNilWhenTheTransportIsNotANetConn(t *testing.T) {
	if got := rawConn(&gortmplib.ServerConn{RW: &bytes.Buffer{}}); got != nil {
		t.Errorf("rawConn over a plain buffer = %v; want nil", got)
	}
	if got := rawConn(&gortmplib.ServerConn{}); got != nil {
		t.Errorf("rawConn with no transport = %v; want nil", got)
	}
}

// With nothing to send, writeMessages must report success without touching the
// connection, so a nil connection is safe here.
func TestWriteMessagesWithNothingToSendSucceeds(t *testing.T) {
	if !writeMessages(nil, nil) {
		t.Error("writeMessages(no messages) = false; an empty write is not a failed one")
	}
}
