package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
)

// The listener is dual-stack, and one IPv6 host owns a whole /64. Keyed on the
// full address, a single VPS could rotate through its own addresses and never
// meet the throttle. Six wrong passwords from six addresses in one /64 are six
// failures by one client.
func TestSixBadLoginsFromOneSlash64Get429(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})

	for i := 1; i <= 6; i++ {
		addr := fmt.Sprintf("[2001:db8:5:6::%x]:44444", i)
		if code := attemptLogin(t, h, addr, "", "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401 inside the allowance", i, code)
		}
	}
	if code := attemptLogin(t, h, "[2001:db8:5:6::99]:44444", "", testPassword); code != http.StatusTooManyRequests {
		t.Fatalf("a seventh address in the same /64 got %d, want 429 -- the /64 is not throttled as one client", code)
	}
	if code := attemptLogin(t, h, "[2001:db8:5:7::1]:44444", "", testPassword); code != http.StatusOK {
		t.Errorf("the neighbouring /64 got %d, want 200 -- it is somebody else", code)
	}
}
