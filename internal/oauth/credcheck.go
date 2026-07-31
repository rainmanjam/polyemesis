package oauth

// Credential checking.
//
// An operator pastes a client ID and secret into Settings, and until now
// nothing verified them: handlePutCreds checked only that two strings were
// non-empty, so a typo was accepted without comment and surfaced much later as
// an opaque platform error at go-live. This file closes that gap for the three
// platforms that can be asked.
//
// The asymmetry is deliberate and load-bearing. Twitch, Kick and Facebook all
// support a client-credentials grant, which proves BOTH halves of the pair with
// no user consent. Google offers no equivalent, so YouTube can only be checked
// for shape. Reporting both as a green tick would be a lie told by a progress
// indicator, so the result carries the method as well as the verdict.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// checkTimeout bounds an outbound credential check. The settings page waits on
// this, so a platform having a bad day must not become polyemesis having one.
const checkTimeout = 5 * time.Second

// CheckState is the verdict. Four states rather than a boolean, because
// "we could not reach the platform" and "the platform said no" are different
// facts and collapsing them ships a frightening, incorrect message.
type CheckState string

const (
	// CheckVerified means the platform accepted the credential pair.
	CheckVerified CheckState = "verified"
	// CheckUnverified means this platform offers no way to check; the verdict
	// arrives at first connect.
	CheckUnverified CheckState = "unverified"
	// CheckRejected means the platform refused the pair.
	CheckRejected CheckState = "rejected"
	// CheckUnreachable means the check could not run. NOT a credential verdict.
	CheckUnreachable CheckState = "unreachable"
)

// CheckMethod names how a verdict was reached, so the UI can say what a pass
// actually proves.
type CheckMethod string

const (
	MethodClientCredentials CheckMethod = "client_credentials"
	MethodFormat            CheckMethod = "format"
)

// CheckResult is what the API returns. Detail is operator-facing and must never
// contain the credential that was checked.
type CheckResult struct {
	Platform db.Platform `json:"platform"`
	State    CheckState  `json:"state"`
	Method   CheckMethod `json:"method"`
	Detail   string      `json:"detail"`
}

// CredentialChecker is implemented by providers whose client credentials can be
// proven correct without a user consent round-trip.
//
// It is deliberately a separate interface rather than an optional method or a
// nil-able func field on a config struct. A nil hook guarded by `if x != nil`
// is how signature verification on the Kick webhook stayed switched off for two
// releases: the field existed, the guard existed, and no construction site ever
// assigned it. A type assertion plus the categorisation test in credcheck_test.go
// makes the same omission impossible to ship.
type CredentialChecker interface {
	// CheckCredentials returns nil when the platform accepts the pair. A
	// returned error must distinguish refusal from unreachability by wrapping
	// ErrCheckUnreachable for the latter.
	CheckCredentials(ctx context.Context, clientID, clientSecret string) error
}

// ErrCheckUnreachable marks a check that could not be performed, as opposed to
// one that was performed and failed.
var ErrCheckUnreachable = fmt.Errorf("the platform could not be reached")

// unverifiableProviders names every provider that cannot be checked, with the
// reason. An entry here is a claim about the PLATFORM, not about the state of
// this code, so it should change only when the platform does.
var unverifiableProviders = map[db.Platform]string{
	db.PlatformYouTube: "Google offers no way to validate a client ID and secret " +
		"without a user consent round-trip. The credentials are checked for shape " +
		"only; the real verdict arrives the first time you connect an account.",
}

// CheckCredentialsFor dispatches to the provider, or reports honestly that no
// check was possible.
func CheckCredentialsFor(ctx context.Context, p db.Platform, clientID, clientSecret string) CheckResult {
	res := CheckResult{Platform: p}

	provider, err := Get(p)
	if err != nil {
		res.State, res.Method, res.Detail = CheckRejected, MethodFormat, err.Error()
		return res
	}

	checker, ok := provider.(CredentialChecker)
	if !ok {
		res.State, res.Method = CheckUnverified, MethodFormat
		res.Detail = unverifiableProviders[p]
		if bad := formatComplaint(p, clientID, clientSecret); bad != "" {
			res.State, res.Detail = CheckRejected, bad
		}
		return res
	}

	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	res.Method = MethodClientCredentials
	switch err := checker.CheckCredentials(ctx, clientID, clientSecret); {
	case err == nil:
		res.State, res.Detail = CheckVerified, "The platform accepted these credentials."
	case errorsIsUnreachable(err):
		res.State, res.Detail = CheckUnreachable, err.Error()
	default:
		res.State, res.Detail = CheckRejected, err.Error()
	}
	return res
}

// formatComplaint is the weak fallback for providers that cannot be checked
// properly. It returns "" when nothing is obviously wrong -- absence of a
// complaint is NOT evidence the credentials work, which is why the state stays
// "unverified" rather than becoming "verified".
func formatComplaint(p db.Platform, clientID, clientSecret string) string {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return "Both a client ID and a client secret are required."
	}
	if p == db.PlatformYouTube && !strings.HasSuffix(clientID, ".apps.googleusercontent.com") {
		return "A Google client ID normally ends in .apps.googleusercontent.com. " +
			"Check you copied the Client ID rather than the project name or the API key."
	}
	return ""
}

// errorsIsUnreachable keeps the errors import local to one helper so the
// dispatch above reads as a decision table rather than error plumbing.
func errorsIsUnreachable(err error) bool {
	return errors.Is(err, ErrCheckUnreachable)
}

// classifyCheckError turns a token-endpoint failure into the distinction the
// UI needs: did the platform refuse the credentials, or could we not ask?
//
// A 5xx or a transport failure is OUR problem to report, not the operator's
// credential to doubt. Telling somebody their secret is wrong when the platform
// merely had a bad minute is the specific wrong message this exists to avoid.
//
// Classification is numeric, not textual: errors.As recovers the status code
// tokenStatusError carries, rather than string-matching it back out of an
// error message. The earlier version matched only " 500", " 502", " 503",
// " 504" and " 429" as substrings, which silently let a 501, a 505, or any of
// Cloudflare's 520-526 fall through and be reported as a bad credential --
// exactly the wrong message this function exists to prevent, for outages that
// substring list simply never named.
func classifyCheckError(err error) error {
	if err == nil {
		return nil
	}

	var se *tokenStatusError
	if errors.As(err, &se) {
		// >=500 is the platform's own server breaking; 429 is it rate-limiting
		// us. Neither says anything about whether the credential pair is
		// correct. Every other status (400, 401, 403, 404...) is the
		// platform's considered answer about the pair itself, and stands.
		if se.code >= 500 || se.code == http.StatusTooManyRequests {
			return fmt.Errorf("%w: %s", ErrCheckUnreachable, err.Error())
		}
		return err
	}

	if isTransportFailure(err) {
		return fmt.Errorf("%w: %s", ErrCheckUnreachable, err.Error())
	}
	return err
}

// isTransportFailure reports whether err means the platform was never reached
// at all, as distinct from a tokenStatusError -- which means it was reached
// and answered.
//
// Structural checks come first because they survive wrapping (an
// http.Client.Do failure arrives as *url.Error wrapping the real cause, a
// dial or DNS failure as *net.OpError, a context cancellation satisfies
// errors.Is against context.DeadlineExceeded) and so hold up regardless of
// which Go version or platform happens to phrase the underlying message.
// The substring fallback at the end exists only for "i/o timeout": Go spells
// a timed-out read or write that way in the message text without giving any
// caller a typed or interface handle on it that isn't already covered by the
// net.Error branch above for the common case -- this is the residual case
// where that check does not apply but the wording still does.
func isTransportFailure(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	return strings.Contains(err.Error(), "i/o timeout")
}
