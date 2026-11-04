//go:build windows

package delayline

import (
	"errors"
	"net"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// errQueueEmpty is readQueued's "nothing is waiting": the drain is over.
var errQueueEmpty = errors.New("delayline: receive queue empty")

// fionread is Winsock's FIONREAD, _IOR('f', 127, u_long). x/sys/windows does
// not export it.
const fionread = 0x4004667f

// readQueued reads one datagram that is ALREADY in the socket's receive queue,
// and answers errQueueEmpty -- without waiting -- when there is none.
//
// Go's Windows sockets are overlapped, not non-blocking, so a bare recv would
// sleep on an empty queue. FIONREAD asks first. On a datagram socket Winsock
// answers with the size of the FIRST queued datagram, so zero means either an
// empty queue or a zero-length datagram at its head; both end the drain. The
// hub never forwards a zero-length datagram (FFmpeg reads one as EOF), so on
// the line's own input only the first case occurs.
//
// The Read that follows has a datagram waiting and returns at once. It still
// gets a deadline: nothing should take it away between the two calls, but a
// drain that could block forever on "should" is the bug drainBudget exists to
// prevent. The deadline is a hang guard, not a timing assumption.
func readQueued(conn *net.UDPConn, buf []byte) (int, error) {
	rc, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pending, returned uint32
	var ierr error
	if cerr := rc.Control(func(fd uintptr) {
		ierr = windows.WSAIoctl(windows.Handle(fd), fionread, nil, 0,
			(*byte)(unsafe.Pointer(&pending)), uint32(unsafe.Sizeof(pending)), &returned, nil, 0)
	}); cerr != nil {
		return 0, cerr
	}
	if ierr != nil {
		return 0, ierr
	}
	if pending == 0 {
		return 0, errQueueEmpty
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	n, err := conn.Read(buf)
	if errors.Is(err, net.ErrClosed) {
		return 0, err
	}
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return 0, errQueueEmpty
		}
	}
	return n, err
}
