package oauth

import (
	"context"
	"errors"
	"fmt"
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

// CheckCredentialsFor is the file's primary exported entry point, and every
// branch of it is reachable today via YouTube -- it is already registered and
// already in unverifiableProviders, so Task 2's Twitch/Kick/Facebook methods
// are not required to exercise the dispatch, the empty-field guard, or the
// format complaint.
func TestCheckCredentialsForYouTube(t *testing.T) {
	const (
		secret = "s3cr3t-do-not-leak-me"
	)

	cases := []struct {
		name         string
		clientID     string
		clientSecret string
		wantState    CheckState
		wantMethod   CheckMethod
	}{
		{
			name:         "empty client ID",
			clientID:     "",
			clientSecret: secret,
			wantState:    CheckRejected,
			wantMethod:   MethodFormat,
		},
		{
			name:         "empty client secret",
			clientID:     "12345.apps.googleusercontent.com",
			clientSecret: "",
			wantState:    CheckRejected,
			wantMethod:   MethodFormat,
		},
		{
			name:         "malformed client ID",
			clientID:     "not-a-google-client-id",
			clientSecret: secret,
			wantState:    CheckRejected,
			wantMethod:   MethodFormat,
		},
		{
			name:         "well-formed pair",
			clientID:     "12345.apps.googleusercontent.com",
			clientSecret: secret,
			wantState:    CheckUnverified,
			wantMethod:   MethodFormat,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckCredentialsFor(context.Background(), db.PlatformYouTube, tc.clientID, tc.clientSecret)

			if got.State != tc.wantState {
				t.Errorf("State = %q, want %q", got.State, tc.wantState)
			}
			if got.Method != tc.wantMethod {
				t.Errorf("Method = %q, want %q", got.Method, tc.wantMethod)
			}
			if strings.TrimSpace(got.Detail) == "" {
				t.Error("Detail is empty; it is what the UI renders instead of a verdict")
			}
			// Global constraint: the secret must never come back in a
			// UI-rendered field, however the check turned out. Pinned here,
			// at the moment a rejection message is built, because that is
			// the code path most likely to interpolate the input it just
			// rejected.
			if tc.clientSecret != "" && strings.Contains(got.Detail, tc.clientSecret) {
				t.Errorf("Detail contains the client secret: %q", got.Detail)
			}
		})
	}
}

// Every registered provider must be either checkable or explicitly declared
// unverifiable -- never both, never neither.
//
// This is the guard that stops a provider added later from defaulting into
// "we could not check this" by omission. That shape of omission has already
// produced one real defect here: KickConfig carried an optional Verify hook
// that no construction site ever set, so signature checking silently never
// ran. An optional security control is an absent one.
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

// classifyCheckError used to string-match a handful of status codes out of an
// error message, and the list was incomplete: a 501, a 505, or any of
// Cloudflare's 520-526 fell through and were reported as a bad credential
// instead of an unreachable platform. This pins the numeric classification
// that replaced it against the specific codes the finding named.
func TestClassifyCheckErrorByStatusCode(t *testing.T) {
	unreachable := []int{429, 500, 501, 502, 503, 504, 520, 525}
	rejected := []int{400, 401, 403, 404}

	for _, code := range unreachable {
		t.Run(fmt.Sprintf("%d is unreachable", code), func(t *testing.T) {
			err := classifyCheckError(&tokenStatusError{code: code, body: "platform said something"})
			if !errors.Is(err, ErrCheckUnreachable) {
				t.Errorf("status %d: errors.Is(err, ErrCheckUnreachable) = false, want true (err = %v)",
					code, err)
			}
		})
	}
	for _, code := range rejected {
		t.Run(fmt.Sprintf("%d is rejected", code), func(t *testing.T) {
			err := classifyCheckError(&tokenStatusError{code: code, body: "invalid client"})
			if errors.Is(err, ErrCheckUnreachable) {
				t.Errorf("status %d: errors.Is(err, ErrCheckUnreachable) = true, want false (err = %v)",
					code, err)
			}
		})
	}
}

// TestTokenStatusErrorTextIsUnchanged pins tokenStatusError.Error() against
// the fmt.Errorf string it replaced in postForm. Nothing that reads the
// message -- classifyCheckError's own %w wrapping included -- should be able
// to tell the difference.
func TestTokenStatusErrorTextIsUnchanged(t *testing.T) {
	err := &tokenStatusError{code: 503, body: "platform is down"}
	want := "token endpoint returned 503: platform is down"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
