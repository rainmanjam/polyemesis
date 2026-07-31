# Guided OAuth Self-Registration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a mistyped OAuth credential or an unusable redirect URI fail loudly at paste time instead of silently at go-live.

**Architecture:** A `CredentialChecker` interface in `internal/oauth`, implemented by the three providers that support a client-credentials grant (Twitch, Kick, Facebook) and explicitly declined by YouTube. `internal/api` gains a re-check route and reports a four-state verdict; the guides endpoint gains server-computed redirect-URI warnings. `SettingsPage.tsx` renders both.

**Tech Stack:** Go 1.26 (no new dependencies), React + TypeScript, oxlint, `httptest` for provider fakes.

**Spec:** [2026-07-30-guided-oauth-self-registration-design.md](../specs/2026-07-30-guided-oauth-self-registration-design.md)

## Global Constraints

- **No new Go dependencies.** `golang.org/x/oauth2/clientcredentials` was considered and rejected in the spec; Facebook's app-token endpoint is a GET and falls outside it.
- **Never log, echo, or return a client secret.** Checks return a verdict and a reason, never their input.
- **A failed check never blocks saving.** The operator may be mid-way through console setup.
- **All checks bounded at 5 seconds** so a hanging platform cannot hang the settings page.
- **Four verdict states**, spelled exactly: `verified`, `unverified`, `rejected`, `unreachable`.
- CI gates, in the order CI runs them: `gofmt -l ./cmd ./internal` must print nothing; `go build ./...`; `go vet ./...`; `go test -race ./...`; then `cd ui && npx tsc -b --noEmit && npm run lint && npm run build`.
- Existing house style: comments explain *why*, and especially *why not*.

---

### Task 1: The CredentialChecker interface and the unverifiable set

**Files:**
- Create: `internal/oauth/credcheck.go`
- Create: `internal/oauth/credcheck_test.go`

**Interfaces:**
- Consumes: `Providers() map[db.Platform]Provider` from `internal/oauth/oauth.go:98`.
- Produces: `CredentialChecker` interface; `CheckState` string type with the four constants; `CheckResult` struct; `unverifiableProviders map[db.Platform]string`; `CheckCredentialsFor(ctx, p db.Platform, clientID, clientSecret string) CheckResult`.

- [ ] **Step 1: Write the failing test**

Create `internal/oauth/credcheck_test.go`:

```go
package oauth

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// NOTE: the whole-registry categorisation guard
// (TestEveryProviderIsEitherCheckableOrDeclaredUnverifiable) lives in Task 2,
// not here. It cannot pass until Twitch, Kick and Facebook have their methods,
// and committing a knowingly-red test would leave one commit on this branch
// that does not build green -- breaking git bisect for no benefit, since Task 2
// follows immediately. The guard still lands before any code depends on it.

// A provider declared unverifiable must say WHY. The reason is rendered to the
// operator as the explanation for an "unverified" badge, so an empty one is a
// blank space in the UI.
func TestUnverifiableProvidersAllCarryAReason(t *testing.T) {
	for platform, reason := range unverifiableProviders {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s is declared unverifiable with an empty reason; the reason "+
				"is the point -- it is what the UI shows instead of a verdict", platform)
		}
	}
}

func TestUnverifiableProvidersOnlyNamesRegisteredPlatforms(t *testing.T) {
	for platform := range unverifiableProviders {
		if _, ok := Providers()[platform]; !ok {
			t.Errorf("unverifiableProviders names %q, which is not a registered provider", platform)
		}
	}
}

func TestYouTubeIsTheDeclaredUnverifiableOne(t *testing.T) {
	// Pins the specific fact the UI depends on. If Google ever ships a
	// credential-check endpoint, this failing is the prompt to implement it.
	if _, ok := unverifiableProviders[db.PlatformYouTube]; !ok {
		t.Fatal("YouTube is expected to be unverifiable: Google offers no way to " +
			"validate a client ID/secret pair without a user consent round-trip")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/oauth/ -run 'TestUnverifiable|TestYouTubeIsThe' -v`
Expected: FAIL to build with `undefined: unverifiableProviders`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/oauth/credcheck.go`:

```go
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
	"fmt"
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
```

- [ ] **Step 4: Add the unreachable helper**

Append to `internal/oauth/credcheck.go`:

```go
// errorsIsUnreachable keeps the errors import local to one helper so the
// dispatch above reads as a decision table rather than error plumbing.
func errorsIsUnreachable(err error) bool {
	return errors.Is(err, ErrCheckUnreachable)
}
```

Add `"errors"` to the import block.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/oauth/ -count=1`
Expected: PASS — the whole package, not just the new tests. All three tests in this task are green on arrival. The whole-registry categorisation guard is deliberately **not** here; it lands in Task 2, where it can also be green. Committing a knowingly-red test would leave one commit on this branch that does not build, breaking `git bisect` for no benefit.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/oauth/credcheck.go internal/oauth/credcheck_test.go
git add internal/oauth/credcheck.go internal/oauth/credcheck_test.go
git commit -m "feat(oauth): CredentialChecker interface and the unverifiable set

Providers that can prove a credential pair without user consent implement
CredentialChecker; those that cannot are named in unverifiableProviders WITH
the reason, because that reason is what the UI shows an operator instead of a
verdict.

Deliberately an interface rather than an optional nil-able hook. That shape is
what left Kick webhook signature verification switched off for two releases:
the field existed, the nil guard existed, and no construction site assigned it.

The whole-registry categorisation guard lands with the provider methods in the
next commit rather than here. It cannot pass until all four are categorised,
and committing it red would leave a commit on this branch that does not build."
```

---

### Task 2: Implement the check for Twitch, Kick and Facebook

**Files:**
- Modify: `internal/oauth/twitch.go`
- Modify: `internal/oauth/kick.go`
- Modify: `internal/oauth/facebook.go`
- Create: `internal/oauth/credcheck_providers_test.go`
- Modify: `internal/oauth/credcheck_test.go` (adds the categorisation guard, Step 9)

**Interfaces:**
- Consumes: `CredentialChecker`, `ErrCheckUnreachable` from Task 1.
- Produces: `(*Twitch).CheckCredentials`, `(*Kick).CheckCredentials`, `(*Facebook).CheckCredentials`, each `(ctx context.Context, clientID, clientSecret string) error`.

- [ ] **Step 1: Write the failing test**

Create `internal/oauth/credcheck_providers_test.go`:

```go
package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// checkFixture swaps the shared httpClient for one pointed at a fake platform,
// and restores it afterwards. The package uses one httpClient (oauth.go:117),
// so this is the seam available without restructuring.
func checkFixture(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	prev := httpClient
	httpClient = srv.Client()
	t.Cleanup(func() {
		httpClient = prev
		srv.Close()
	})
	return srv
}

func TestTwitchCheckCredentials(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantErr     bool
		unreachable bool
	}{
		{name: "accepted", status: 200, body: `{"access_token":"a","expires_in":3600}`},
		{name: "bad secret", status: 403, body: `{"status":403,"message":"invalid client secret"}`, wantErr: true},
		{name: "bad client id", status: 400, body: `{"status":400,"message":"invalid client"}`, wantErr: true},
		{name: "platform broken", status: 500, body: `oops`, wantErr: true, unreachable: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := checkFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if got := r.FormValue("grant_type"); got != "client_credentials" {
					t.Errorf("grant_type = %q, want client_credentials", got)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			tw := &Twitch{tokenURL: srv.URL}

			err := tw.CheckCredentials(context.Background(), "id", "secret")
			if tc.wantErr && err == nil {
				t.Fatal("accepted, want an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("rejected a valid pair: %v", err)
			}
			if got := errors.Is(err, ErrCheckUnreachable); got != tc.unreachable {
				t.Fatalf("errors.Is(err, ErrCheckUnreachable) = %v, want %v (err = %v)",
					got, tc.unreachable, err)
			}
		})
	}
}

func TestCheckCredentialsNeverEchoesTheSecret(t *testing.T) {
	// The detail string is rendered in the UI. It must never carry the input.
	const secret = "s3cr3t-do-not-print-me"
	srv := checkFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"message":"invalid client secret"}`))
	})
	tw := &Twitch{tokenURL: srv.URL}

	err := tw.CheckCredentials(context.Background(), "id", secret)
	if err == nil {
		t.Fatal("expected rejection")
	}
	if contains(err.Error(), secret) {
		t.Fatalf("the error text contains the secret: %q", err.Error())
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/oauth/ -run TestTwitchCheckCredentials -v`
Expected: FAIL to build — `unknown field tokenURL in struct literal` and `tw.CheckCredentials undefined`.

- [ ] **Step 3: Make Twitch's token URL injectable and add the check**

In `internal/oauth/twitch.go`, add a field to the `Twitch` struct and a resolver:

```go
// tokenURL overrides Twitch's token endpoint. Empty in production; set by
// tests so a credential check can be exercised without reaching id.twitch.tv.
tokenURL string

// twitchTokenURL is the real endpoint, and the default when tokenURL is unset.
const twitchTokenURL = "https://id.twitch.tv/oauth2/token"

func (t *Twitch) tokenEndpoint() string {
	if t.tokenURL != "" {
		return t.tokenURL
	}
	return twitchTokenURL
}
```

Replace the two existing literal uses of `"https://id.twitch.tv/oauth2/token"` (twitch.go:93 and twitch.go:103) with `t.tokenEndpoint()`.

Then append the check:

```go
// CheckCredentials proves both halves of the pair via a client-credentials
// grant, which Twitch supports and which needs no user consent. The app token
// it returns is discarded: obtaining one at all is the whole proof.
func (t *Twitch) CheckCredentials(ctx context.Context, clientID, clientSecret string) error {
	_, err := postForm(ctx, t.tokenEndpoint(), url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"grant_type":    {"client_credentials"},
	}, nil)
	return classifyCheckError(err)
}
```

- [ ] **Step 4: Add the shared error classifier**

Append to `internal/oauth/credcheck.go`:

```go
// classifyCheckError turns a token-endpoint failure into the distinction the
// UI needs: did the platform refuse the credentials, or could we not ask?
//
// A 5xx or a transport failure is OUR problem to report, not the operator's
// credential to doubt. Telling somebody their secret is wrong when the platform
// merely had a bad minute is the specific wrong message this exists to avoid.
func classifyCheckError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	// postForm formats non-2xx as "token endpoint returned %d: %s".
	for _, code := range []string{" 500", " 502", " 503", " 504", " 429"} {
		if strings.Contains(msg, "returned"+code) {
			return fmt.Errorf("%w: %s", ErrCheckUnreachable, msg)
		}
	}
	if strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "connection refused") {
		return fmt.Errorf("%w: %s", ErrCheckUnreachable, msg)
	}
	return err
}
```

- [ ] **Step 5: Run the Twitch tests**

Run: `go test ./internal/oauth/ -run 'TestTwitchCheckCredentials|TestCheckCredentialsNeverEchoes' -v`
Expected: PASS, all four subtests plus the secret-echo test.

- [ ] **Step 6: Add Kick's check**

In `internal/oauth/kick.go`, add the same seam and method. `kickIDBase` is already a package const (kick.go:68); add an override field on `Kick`:

```go
// idBase overrides https://id.kick.com. Empty in production; set by tests.
idBase string

func (k *Kick) idEndpoint() string {
	if k.idBase != "" {
		return k.idBase
	}
	return kickIDBase
}

// CheckCredentials proves the pair through Kick's app-access-token flow, which
// its OAuth 2.1 documentation exposes at POST /oauth/token with
// grant_type=client_credentials and needs no user consent.
func (k *Kick) CheckCredentials(ctx context.Context, clientID, clientSecret string) error {
	_, err := postForm(ctx, k.idEndpoint()+"/oauth/token", url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"grant_type":    {"client_credentials"},
	}, nil)
	return classifyCheckError(err)
}
```

Replace the two existing `kickIDBase+"/oauth/token"` uses (kick.go:150 and kick.go:154) with `k.idEndpoint()+"/oauth/token"`, and `kickIDBase + "/oauth/authorize?"` (kick.go:133) with `k.idEndpoint() + "/oauth/authorize?"`.

- [ ] **Step 7: Add Facebook's check**

Facebook's app token is a **GET**, unlike the others, so it cannot reuse `postForm`. In `internal/oauth/facebook.go`:

```go
// graphBase overrides https://graph.facebook.com/v24.0. Empty in production.
graphBase string

func (f *Facebook) graphEndpoint() string {
	if f.graphBase != "" {
		return f.graphBase
	}
	return fbGraphBase
}

// CheckCredentials proves the pair through Facebook's app-access-token
// endpoint. Note the shape: this one is a GET with query parameters, where
// Twitch and Kick both POST a form. Reusing postForm here would send a request
// Facebook answers with 400 regardless of whether the credentials are good,
// which would report every correct pair as rejected.
func (f *Facebook) CheckCredentials(ctx context.Context, clientID, clientSecret string) error {
	q := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"grant_type":    {"client_credentials"},
	}
	endpoint := f.graphEndpoint() + "/oauth/access_token?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return classifyCheckError(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return classifyCheckError(fmt.Errorf("token endpoint returned %d: %s",
			resp.StatusCode, snippet(body)))
	}
	return nil
}
```

- [ ] **Step 8: Add Kick and Facebook tests**

Append to `internal/oauth/credcheck_providers_test.go`:

```go
func TestKickCheckCredentials(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		srv := checkFixture(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/oauth/token" {
				t.Errorf("path = %q, want /oauth/token", r.URL.Path)
			}
			if got := r.FormValue("grant_type"); got != "client_credentials" {
				t.Errorf("grant_type = %q", got)
			}
			_, _ = w.Write([]byte(`{"access_token":"a","expires_in":3600}`))
		})
		k := &Kick{idBase: srv.URL}
		if err := k.CheckCredentials(context.Background(), "id", "secret"); err != nil {
			t.Fatalf("rejected a valid pair: %v", err)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		srv := checkFixture(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
		})
		k := &Kick{idBase: srv.URL}
		err := k.CheckCredentials(context.Background(), "id", "wrong")
		if err == nil {
			t.Fatal("accepted, want rejection")
		}
		if errors.Is(err, ErrCheckUnreachable) {
			t.Fatal("a 401 is a refusal, not an unreachable platform")
		}
	})
}

func TestFacebookCheckCredentialsUsesGET(t *testing.T) {
	// The shape matters: Facebook answers a POSTed form with 400 whatever the
	// credentials are, so reusing postForm would report every correct pair as
	// rejected.
	var method, path string
	srv := checkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`{"access_token":"app-token"}`))
	})
	f := &Facebook{graphBase: srv.URL}

	if err := f.CheckCredentials(context.Background(), "id", "secret"); err != nil {
		t.Fatalf("rejected a valid pair: %v", err)
	}
	if method != http.MethodGet {
		t.Errorf("method = %s, want GET", method)
	}
	if path != "/oauth/access_token" {
		t.Errorf("path = %q, want /oauth/access_token", path)
	}
}
```

- [ ] **Step 9: Add the whole-registry categorisation guard**

Now that all four providers are categorised, this guard can be written green.
Append to `internal/oauth/credcheck_test.go`:

```go
// Every registered provider must be either checkable or explicitly declared
// unverifiable -- never both, never neither.
//
// This is the guard that stops a provider added later from defaulting into
// "we could not check this" by omission. The same shape of omission produced
// the Medium finding in the 2026-07-30 security review: KickConfig carried an
// optional Verify hook that no construction site ever set, so signature
// checking silently never ran. An optional security control is an absent one.
func TestEveryProviderIsEitherCheckableOrDeclaredUnverifiable(t *testing.T) {
	for platform, provider := range Providers() {
		_, checkable := provider.(CredentialChecker)
		_, declared := unverifiableProviders[platform]

		switch {
		case checkable && declared:
			t.Errorf("%s implements CredentialChecker AND is listed as unverifiable; "+
				"pick one", platform)
		case !checkable && !declared:
			t.Errorf("%s neither implements CredentialChecker nor appears in "+
				"unverifiableProviders. Add the method, or add an entry saying why "+
				"the platform offers no way to verify a credential without user "+
				"consent.", platform)
		}
	}
}
```

- [ ] **Step 9b: Run the whole package**

Run: `go test ./internal/oauth/ -count=1`
Expected: PASS, including the new guard — all four providers are now categorised.

- [ ] **Step 10: Mutation-test the categorisation guard**

Temporarily delete the `CheckCredentials` method from `internal/oauth/twitch.go`.
Run: `go test ./internal/oauth/ -run TestEveryProviderIsEither -count=1`
Expected: FAIL naming twitch.
Restore the method with `git checkout internal/oauth/twitch.go` **only if nothing else in that file is uncommitted** — otherwise re-add by hand. Re-run and confirm PASS.

- [ ] **Step 11: Commit**

```bash
gofmt -w internal/oauth/
go test ./internal/oauth/ -count=1
git add internal/oauth/
git commit -m "feat(oauth): verify Twitch, Kick and Facebook credentials on paste

All three support a client-credentials grant, which proves both halves of the
pair with no user consent. The app token returned is discarded; obtaining one
at all is the proof.

Facebook's endpoint is a GET with query parameters where the other two POST a
form, and it answers a POSTed form with 400 regardless of whether the
credentials are correct -- so reusing postForm there would have reported every
valid pair as rejected.

classifyCheckError keeps 5xx, 429 and transport failures out of the 'your
credentials are wrong' bucket. Telling an operator their secret is bad when the
platform merely had a bad minute is the specific wrong message this avoids."
```

---

### Task 3: The API surface — validate on save, and a re-check route

**Files:**
- Modify: `internal/api/oauth_handlers.go:65-87` (`handlePutCreds`)
- Modify: `internal/api/api.go:376` (add the check route)
- Create: `internal/api/credcheck_test.go`

**Interfaces:**
- Consumes: `oauth.CheckCredentialsFor`, `oauth.CheckResult` from Task 1.
- Produces: `POST /api/v1/platforms/credentials/{platform}/check`; `handlePutCreds` response now embeds `oauth.CheckResult` under `"check"`.

- [ ] **Step 1: Write the failing test**

Create `internal/api/credcheck_test.go`:

```go
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A rejected credential must still be stored. An operator is often mid-way
// through console setup, and refusing to save a credential they are about to
// make valid is obstructive rather than protective.
func TestSaveStoresEvenWhenTheCheckRejects(t *testing.T) {
	s := newTestServer(t)

	body := strings.NewReader(`{"clientId":"nope","clientSecret":"also-nope"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/platforms/credentials/youtube", body)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authed(t, s, req))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a failed check must not block saving", w.Code)
	}

	var got struct {
		Platform string `json:"platform"`
		Check    struct {
			State  string `json:"state"`
			Method string `json:"method"`
			Detail string `json:"detail"`
		} `json:"check"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Check.State != "rejected" {
		t.Errorf("state = %q, want rejected: a Google client ID must end in "+
			".apps.googleusercontent.com", got.Check.State)
	}
	if got.Check.Detail == "" {
		t.Error("no detail; the operator needs to know what to fix")
	}
}

func TestSaveReportsUnverifiableHonestly(t *testing.T) {
	s := newTestServer(t)

	body := strings.NewReader(`{"clientId":"1234.apps.googleusercontent.com","clientSecret":"x"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/platforms/credentials/youtube", body)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authed(t, s, req))

	var got struct {
		Check struct {
			State  string `json:"state"`
			Method string `json:"method"`
		} `json:"check"`
	}
	_ = json.NewDecoder(w.Body).Decode(&got)

	if got.Check.State != "unverified" {
		t.Fatalf("state = %q, want unverified: Google cannot be asked, and "+
			"reporting a format check as 'verified' would be a lie", got.Check.State)
	}
	if got.Check.Method != "format" {
		t.Errorf("method = %q, want format", got.Check.Method)
	}
}

func TestCheckRouteExists(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/platforms/credentials/youtube/check", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authed(t, s, req))

	if w.Code == http.StatusNotFound {
		t.Fatal("no re-check route; an operator who fixes something in the console " +
			"must be able to retest without re-pasting")
	}
}
```

- [ ] **Step 2: Build the test helpers — they do not exist yet**

Verified: `internal/api` has no `newTestServer` or `authed` helper. Tests construct a server directly with `New(Options{...})`.

Read `internal/api/token_handlers_test.go:42` and copy its setup verbatim into `credcheck_test.go` as `newTestServer(t *testing.T) *Server`, and its authenticated-request pattern as `authed(t *testing.T, s *Server, r *http.Request) *http.Request`. That file is the closest model because it also exercises authenticated credential-shaped routes.

Do not invent a new fixture pattern; the point is that these tests look like their neighbours.

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/api/ -run 'TestSaveStores|TestSaveReports|TestCheckRouteExists' -v`
Expected: FAIL — no `check` field in the response, and 404 for the check route.

- [ ] **Step 4: Add validation to handlePutCreds**

In `internal/api/oauth_handlers.go`, replace the final `writeJSON` of `handlePutCreds` with:

```go
	// The check runs AFTER the store succeeds, and its result never changes the
	// status code. An operator part-way through console setup must be able to
	// save a credential they are about to make valid.
	check := oauth.CheckCredentialsFor(r.Context(), platform, req.ClientID, req.ClientSecret)
	writeJSON(w, http.StatusOK, map[string]any{
		"platform":  platform,
		"hasSecret": true,
		"check":     check,
	})
```

- [ ] **Step 5: Add the re-check handler**

Append to `internal/api/oauth_handlers.go`:

```go
// handleCheckCreds re-runs the credential check against what is stored, so an
// operator who has just fixed something in the platform console can retest
// without pasting the secret again.
func (s *Server) handleCheckCreds(w http.ResponseWriter, r *http.Request) {
	platform := db.Platform(chi.URLParam(r, "platform"))
	if _, err := oauth.Get(platform); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	creds, err := s.store.GetPlatformCreds(s.box, platform)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK,
		oauth.CheckCredentialsFor(r.Context(), platform, creds.ClientID, creds.ClientSecret))
}
```

- [ ] **Step 6: Register the route**

In `internal/api/api.go`, immediately after line 376 (`r.Put("/platforms/credentials/{platform}", ...)`), add:

```go
			// POST rather than GET: it makes an outbound call to a third party,
			// so it is neither safe nor idempotent, and POST puts it behind
			// requireCSRF with the rest of the state-changing group.
			r.Post("/platforms/credentials/{platform}/check", s.handleCheckCreds)
```

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/api/ -run 'TestSaveStores|TestSaveReports|TestCheckRouteExists' -count=1 -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
gofmt -w internal/api/
go test ./internal/api/ -count=1
git add internal/api/
git commit -m "feat(api): report a credential verdict on save, and allow re-checking

handlePutCreds previously checked only that two strings were non-empty. It now
returns a four-state verdict alongside the stored result.

The check runs after the store succeeds and never changes the status code: an
operator part-way through console setup must be able to save a credential they
are about to make valid.

The re-check route is POST despite reading nothing, because it makes an
outbound call to a third party and belongs behind requireCSRF."
```

---

### Task 4: Redirect-URI preflight warnings

**Files:**
- Create: `internal/api/redirect_preflight.go`
- Create: `internal/api/redirect_preflight_test.go`
- Modify: `internal/api/oauth_handlers.go:18-30` (`handlePlatformGuides`)
- Modify: `internal/oauth/oauth.go:258-275` (`SetupGuide` struct)

**Interfaces:**
- Consumes: `s.origin(r)` from `internal/api/oauth_handlers.go:33`.
- Produces: `redirectWarnings(cfg config.Config, r *http.Request, redirectURI string) []string`; `SetupGuide.RedirectWarnings []string` with JSON tag `redirectWarnings`.

- [ ] **Step 1: Write the failing test**

Create `internal/api/redirect_preflight_test.go`:

```go
package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
)

func TestRedirectWarnings(t *testing.T) {
	tests := []struct {
		name        string
		cfg         config.Config
		host        string
		forwarded   string
		redirectURI string
		wantSubstr  string // "" means: expect NO warnings
	}{
		{
			name:        "https on a real hostname is fine",
			cfg:         config.Config{TLS: config.TLS{Hostname: "stream.example.com"}},
			host:        "stream.example.com",
			redirectURI: "https://stream.example.com/api/v1/oauth/youtube/callback",
		},
		{
			name:        "loopback over http is fine",
			host:        "localhost:8080",
			redirectURI: "http://localhost:8080/api/v1/oauth/youtube/callback",
		},
		{
			name:        "plain http on a routable host",
			host:        "box.lan",
			redirectURI: "http://box.lan/api/v1/oauth/youtube/callback",
			wantSubstr:  "HTTPS",
		},
		{
			name:        "bare IP address",
			host:        "192.168.1.50:8080",
			redirectURI: "http://192.168.1.50:8080/api/v1/oauth/youtube/callback",
			wantSubstr:  "IP address",
		},
		{
			name:        "browsed host disagrees with configured hostname",
			cfg:         config.Config{TLS: config.TLS{Hostname: "stream.example.com"}},
			host:        "192.168.1.50:8080",
			redirectURI: "http://192.168.1.50:8080/api/v1/oauth/youtube/callback",
			wantSubstr:  "stream.example.com",
		},
		{
			name:        "proxied but proxy headers not trusted",
			host:        "internal:8080",
			forwarded:   "stream.example.com",
			redirectURI: "http://internal:8080/api/v1/oauth/youtube/callback",
			wantSubstr:  "reverse proxy",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/platforms/guides", nil)
			req.Host = tc.host
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-Host", tc.forwarded)
			}

			got := redirectWarnings(tc.cfg, req, tc.redirectURI)

			if tc.wantSubstr == "" {
				if len(got) != 0 {
					t.Fatalf("warned about a usable redirect URI: %v", got)
				}
				return
			}
			joined := strings.Join(got, " | ")
			if !strings.Contains(joined, tc.wantSubstr) {
				t.Fatalf("warnings = %q, want one containing %q", joined, tc.wantSubstr)
			}
			for _, wcase := range got {
				if !strings.Contains(wcase, tc.redirectURI) && !strings.Contains(wcase, "stream.example.com") {
					t.Errorf("warning does not name a URI to register: %q", wcase)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -run TestRedirectWarnings -v`
Expected: FAIL to build, `undefined: redirectWarnings`.

- [ ] **Step 3: Write the implementation**

Create `internal/api/redirect_preflight.go`:

```go
package api

// Redirect-URI preflight.
//
// redirect_uri_mismatch is the single most common way OAuth setup fails, and it
// fails LATE: the operator registers a URI, pastes credentials, clicks Connect,
// and only then learns the URI was wrong. Everything here is an attempt to move
// that discovery earlier, to the moment the URI is displayed.
//
// Every warning names the exact URI to register. A warning that says only
// "this may be wrong" relocates the problem rather than solving it.
//
// Nothing here blocks. A reverse proxy terminating TLS upstream is
// indistinguishable, from inside this process, from a misconfiguration, and
// refusing to proceed would trap a working deployment to protect a hypothetical
// broken one.

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/config"
)

func redirectWarnings(cfg config.Config, r *http.Request, redirectURI string) []string {
	u, err := url.Parse(redirectURI)
	if err != nil || u.Host == "" {
		return nil
	}

	host := u.Hostname()
	var out []string

	if u.Scheme == "http" && !isLoopbackHost(host) {
		out = append(out, fmt.Sprintf(
			"This redirect URI is plain HTTP: %s. Google rejects non-HTTPS redirect "+
				"URIs outright, and Twitch allows them only for localhost. Serve "+
				"polyemesis over HTTPS, or reach it through a proxy that does, before "+
				"registering it.", redirectURI))
	}

	if net.ParseIP(host) != nil {
		out = append(out, fmt.Sprintf(
			"This redirect URI uses a bare IP address: %s. Google will not accept an "+
				"IP for a web application client. Use a hostname.", redirectURI))
	}

	if configured := strings.TrimSpace(cfg.TLS.Hostname); configured != "" && !strings.EqualFold(configured, host) {
		out = append(out, fmt.Sprintf(
			"You are browsing %s, but this server is configured as %s. Register the "+
				"URI for %s or the connection will fail with redirect_uri_mismatch.",
			host, configured, configured))
	}

	if !cfg.TrustProxyHeaders && r.Header.Get("X-Forwarded-Host") != "" {
		out = append(out, fmt.Sprintf(
			"A reverse proxy is forwarding this request, but trustProxyHeaders is off, "+
				"so polyemesis cannot see the address your browser actually used. The "+
				"URI shown (%s) is probably not the one to register.", redirectURI))
	}

	return out
}

// isLoopbackHost reports whether plain HTTP is acceptable for this host.
// Platforms carve out localhost precisely because there is no network hop to
// protect.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
```

Add `"net/http"` to the import block.

- [ ] **Step 4: Run the test**

Run: `go test ./internal/api/ -run TestRedirectWarnings -count=1 -v`
Expected: PASS, all six subtests.

- [ ] **Step 5: Add the field to SetupGuide**

In `internal/oauth/oauth.go`, add to the `SetupGuide` struct after `Note`:

```go
	// RedirectWarnings are computed per request by the API layer, which is
	// where the configuration and the inbound Host live. Empty here; filled in
	// by handlePlatformGuides.
	RedirectWarnings []string `json:"redirectWarnings,omitempty"`
```

- [ ] **Step 6: Populate it in the handler**

In `internal/api/oauth_handlers.go`, inside the existing loop in `handlePlatformGuides`:

```go
	for i := range guides {
		if guides[i].RedirectPath != "" {
			guides[i].RedirectPath = origin + guides[i].RedirectPath
			guides[i].RedirectWarnings = redirectWarnings(s.cfg, r, guides[i].RedirectPath)
		}
	}
```

- [ ] **Step 7: Verify the whole package**

Run: `gofmt -l ./internal && go vet ./internal/... && go test ./internal/api/ ./internal/oauth/ -count=1`
Expected: no gofmt output, no vet output, both packages PASS.

- [ ] **Step 8: Mutation-test one warning**

Temporarily change `if u.Scheme == "http"` to `if false` in `redirect_preflight.go`.
Run: `go test ./internal/api/ -run TestRedirectWarnings -count=1`
Expected: FAIL on "plain http on a routable host".
Restore the line by hand, re-run, confirm PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/api/redirect_preflight.go internal/api/redirect_preflight_test.go internal/api/oauth_handlers.go internal/oauth/oauth.go
git commit -m "feat(api): warn about an unusable redirect URI before it is registered

redirect_uri_mismatch is the most common OAuth setup failure and it surfaces
late: register, paste, connect, and only then learn the URI was wrong. These
four checks move that discovery to the moment the URI is displayed.

Every warning names the exact URI to register, because a warning that says only
'this may be wrong' relocates the problem rather than solving it.

Nothing blocks. A reverse proxy terminating TLS upstream is indistinguishable,
from inside this process, from a misconfiguration, and refusing to proceed
would trap a working deployment to protect a hypothetical broken one."
```

---

### Task 5: The UI surface

**Files:**
- Modify: `ui/src/lib/types.ts:1147-1156` (`SetupGuide`)
- Modify: `ui/src/lib/api.ts:607-615`
- Modify: `ui/src/pages/SettingsPage.tsx` (the platform credentials card, around 1509-1560)

**Interfaces:**
- Consumes: `redirectWarnings` on the guides payload and the `check` object from `PUT /platforms/credentials/{platform}` (Tasks 3 and 4).
- Produces: `api.checkCreds(platform)`; `CredentialCheck` TypeScript interface.

- [ ] **Step 1: Add the types**

In `ui/src/lib/types.ts`, replace the `SetupGuide` interface with:

```ts
export interface SetupGuide {
  platform: Platform;
  name: string;
  consoleUrl: string;
  redirectPath: string;
  steps: string[];
  scopes: string[] | null;
  supported: boolean;
  /** Present when the account connects but the stream key is pasted by hand. */
  manualStreamKey?: boolean;
  note?: string;
  /** Computed per request: reasons the displayed redirect URI may not work. */
  redirectWarnings?: string[];
}

/** The verdict on a pasted client ID and secret.
 *
 *  Four states rather than a boolean because "we could not reach the platform"
 *  and "the platform said no" are different facts, and because YouTube cannot
 *  be checked at all -- rendering its format check as the same tick Twitch
 *  earns would be a lie told by a progress indicator. */
export interface CredentialCheck {
  platform: Platform;
  state: "verified" | "unverified" | "rejected" | "unreachable";
  method: "client_credentials" | "format";
  detail: string;
}
```

`manualStreamKey` is added here because the Go struct has carried it since the
Kick key work and the TypeScript type never gained it.

- [ ] **Step 2: Add the API call**

In `ui/src/lib/api.ts`, after the existing `putCreds` entry:

```ts
  checkCreds: (platform: string) =>
    post<CredentialCheck>(`/platforms/credentials/${platform}/check`, {}),
```

Import `CredentialCheck` alongside the existing type imports. If the module uses a helper other than `post` for bodyless POSTs, match the surrounding style rather than introducing one.

- [ ] **Step 3: Verify types compile**

Run: `cd ui && npx tsc -b --noEmit`
Expected: clean. If `post` does not exist with that signature, read `ui/src/lib/api.ts` around line 600 and use whatever the neighbouring calls use.

- [ ] **Step 4: Render the warnings**

In `ui/src/pages/SettingsPage.tsx`, inside the `guide.redirectPath && (...)` block, immediately after the closing `</span>` of the "Must match exactly" hint:

```tsx
{guide.redirectWarnings?.map((warning, i) => (
  <p
    key={i}
    className="rounded border border-warn/40 bg-warn/10 px-2 py-1.5 text-[10px] text-warn"
  >
    {warning}
  </p>
))}
```

Verified: the `warn` token exists and is already used for exactly this shape of
message. `ui/src/components/DestinationCard.tsx:203` is the pattern to match:

```tsx
<div className="flex items-start gap-1.5 rounded border border-warn/30 bg-warn-dim px-2 py-1 text-[10px] text-warn">
```

Use those classes rather than the approximation above if the two disagree — the
goal is that this warning looks like every other warning in the product.

- [ ] **Step 5: Widen putCreds' return type FIRST**

This comes before the render step because Step 6 reads `saved.check`, and that
does not typecheck until the return type admits it.

`api.putCreds` currently returns `{ platform: string }`. Widen it:

```ts
  putCreds: (platform: string, clientId: string, clientSecret: string) =>
    put<{ platform: string; hasSecret: boolean; check: CredentialCheck }>(
      `/platforms/credentials/${platform}`,
      { clientId, clientSecret },
    ),
```

Keep the existing argument order and names; only the response type changes.

- [ ] **Step 6: Render the check verdict**

Add state near the other `useState` calls in the credentials card component:

```tsx
const [check, setCheck] = useState<CredentialCheck | null>(null);
```

Where credentials are saved, capture the result:

```tsx
const saved = await api.putCreds(guide.platform, clientId, clientSecret);
setCheck(saved.check ?? null);
```

And render it under the credential fields:

```tsx
{check && (
  <div className="flex items-start gap-2 text-[10px]">
    <Badge
      variant={
        check.state === "verified" ? "live"
        : check.state === "rejected" ? "destructive"
        : "outline"
      }
    >
      {check.state === "verified" && "verified"}
      {check.state === "rejected" && "rejected"}
      {check.state === "unverified" && "not verifiable"}
      {check.state === "unreachable" && "could not check"}
    </Badge>
    <span className="text-muted-foreground">{check.detail}</span>
  </div>
)}
```

The four labels are deliberately distinct words. "not verifiable" must never
read as "verified", and "could not check" must never read as "rejected".

- [ ] **Step 7: Run the UI gates**

Run: `cd ui && npx tsc -b --noEmit && npm run lint && npm run build`
Expected: all three clean.

- [ ] **Step 8: Commit**

```bash
git add ui/src/lib/types.ts ui/src/lib/api.ts ui/src/pages/SettingsPage.tsx
git commit -m "feat(ui): show the credential verdict and redirect-URI warnings

Warnings render above the credential fields, because registering the right URI
has to happen before the credentials matter.

The verdict uses four distinct words. 'not verifiable' must never read as
'verified' and 'could not check' must never read as 'rejected' -- a check that
cannot fail must not look like one that can, and a platform outage must not be
reported as the operator's mistake.

Also adds manualStreamKey to the TypeScript SetupGuide, which the Go struct has
carried since the Kick stream-key work while the TS type never gained it."
```

---

### Task 6: Full verification

**Files:** none modified.

- [ ] **Step 1: Run every gate CI runs, in CI's order**

```bash
gofmt -l ./cmd ./internal | tee /tmp/fmt && test ! -s /tmp/fmt
go build ./...
go vet ./...
go test -race ./... 
cd ui && npx tsc -b --noEmit && npm run lint && npm run build
```

Expected: gofmt prints nothing; build, vet silent; 30 packages `ok`; all three UI gates clean.

- [ ] **Step 2: Confirm no secret reaches a log or a response**

Run: `grep -rn "ClientSecret\|clientSecret" internal/api/oauth_handlers.go internal/oauth/credcheck.go`
Expected: the secret appears only as a function argument and in the call to `CheckCredentialsFor`. It must not appear inside any `writeJSON`, `fmt.Errorf`, or `s.log` call.

- [ ] **Step 3: Confirm the spec's four states are all reachable**

Run: `grep -rn "CheckVerified\|CheckUnverified\|CheckRejected\|CheckUnreachable" internal/oauth/credcheck.go`
Expected: each constant is assigned to `res.State` on some path.

- [ ] **Step 4: Commit any formatting drift**

```bash
gofmt -w ./cmd ./internal
git diff --quiet || (git add -A && git commit -m "chore: gofmt")
```

---

## Self-Review

**Spec coverage:**

| Spec section | Task |
|---|---|
| 1. Where validation lives — interface, assertions, exclusion set, drift test | Task 1 |
| 2. What a check means — four states, method reported, re-check route | Tasks 1, 3 |
| 3. Redirect-URI preflight — four checks, never blocking | Task 4 |
| 4. UI — warnings above fields, three-plus-one status states | Task 5 |
| 5. Errors — unreachable distinct, 5s bound, no secret echoed | Tasks 1, 2, 6 |
| 6. Testing — per provider, positive and negative preflight cases, drift, mutation | Tasks 1, 2, 4 |

The spec's compile-time assertion suggestion (`var _ CredentialChecker = (*Twitch)(nil)`) is
covered by the runtime categorisation test instead, which is strictly stronger:
an assertion proves the method exists, while the test also proves the provider
is not simultaneously listed as unverifiable and that its reason is non-empty.

**Placeholder scan:** no TBD/TODO; every code step carries real code. The two
steps that referred vaguely to "an existing pattern" were checked against the
codebase and replaced with concrete references: token_handlers_test.go:42 for
the API fixture (newTestServer and authed do NOT exist and must be written), and
DestinationCard.tsx:203 for the warning styling (the warn token does exist).
ui/src/lib/api.ts:126 confirms the post helper signature the plan uses.

**Type consistency:** `CheckResult`/`CredentialCheck` fields match across Go and
TypeScript (`platform`, `state`, `method`, `detail`). `redirectWarnings` matches
the Go JSON tag. `CheckCredentials(ctx, clientID, clientSecret) error` is
identical in the interface and all three implementations.
