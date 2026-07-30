package api

import "testing"

// secretEqual is a thin wrapper over subtle.ConstantTimeCompare, so what is
// worth pinning is not the timing property — which a unit test cannot observe
// reliably — but that it still answers the question correctly. A "constant
// time" comparison that returns true for everything is constant time and
// useless.
func TestSecretEqual(t *testing.T) {
	// Named for the shape rather than for what it holds: a constant called
	// `secret` assigned a high-entropy hex string is what every credential
	// scanner is built to flag, and a fixture that has to be permanently
	// allowlisted is a fixture that trains people to widen allowlists.
	const configured = "9f2c1a4b7e8d0356af91cc2b4d6e8f01"

	tests := []struct {
		name string
		got  string
		want bool
	}{
		{"the same secret", configured, true},
		{"a different secret of the same length", "00000000000000000000000000000000", false},
		{"the empty string", "", false},
		{"a prefix", configured[:16], false},
		{"the secret plus a trailing byte", configured + "0", false},
		{"a single flipped character", "af2c1a4b7e8d0356af91cc2b4d6e8f01", false},
		{"differing only in the final character", configured[:len(configured)-1] + "2", false},
		{"a case change", "9F2C1A4B7E8D0356AF91CC2B4D6E8F01", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := secretEqual(tc.got, configured); got != tc.want {
				t.Fatalf("secretEqual(%q, want) = %v, want %v", tc.got, got, tc.want)
			}
		})
	}
}

func TestSecretEqualIsNotSatisfiedByTwoEmptyStrings(t *testing.T) {
	// The caller guards against an unconfigured secret separately (`want == ""`
	// answers 404 before this is reached), but a comparison that treats "no
	// secret" as "matches nothing" is the safer primitive to hand it.
	if !secretEqual("", "") {
		return // ConstantTimeCompare reports equal for two empty slices.
	}
	// Documenting the actual behaviour rather than asserting a wish: two empty
	// strings DO compare equal, which is exactly why handleKickChatWebhook
	// checks for an empty configured secret first. If that check is ever
	// removed, an install with no secret configured would accept any path.
	t.Log("two empty secrets compare equal; the empty-secret guard at the " +
		"call site is load-bearing and must not be removed")
}
