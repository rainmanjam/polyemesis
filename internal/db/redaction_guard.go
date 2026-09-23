package db

import (
	"fmt"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

// A CREDENTIAL CARRYING THE REDACTION PLACEHOLDER IS REFUSED ON THE WAY IN.
//
// Readiness review row 16. A read-scoped token sees these fields blanked or
// masked -- backupStreamKey and the two expert-argument fields come back as the
// literal alerts.Mask, a URL with its credential part replaced by it (see
// internal/api/redact.go) -- and docs/API.md says, deliberately, that the
// document keeps its shape so a client can read it, edit it and PUT it back.
// That round-trip is the hazard. The response a read token was handed, replayed
// by anything holding an admin credential, wrote "[redacted]" into the row:
// sealStreamKey seals whatever it is given, so the real backup key was replaced
// by the placeholder and nothing failed until the day the backup was needed --
// a failover, which is the worst possible moment to find out.
//
// Hooks and alert rules answer the same round-trip by reading the mask as
// "unchanged" (applyTo in internal/api), which works there because each is one
// URL field on a request type of its own. These fields arrive by decoding a
// body over the stored row, so "unchanged" cannot be told apart from "replaced
// by the placeholder" after the decode. Refused instead, naming the field, so
// the caller learns which value it has to send for real.
//
// THE SAME CONSTANT THE REDACTION WRITES. alerts.Mask is what redact.go puts in
// these fields, so the spelling this refuses cannot drift from the spelling a
// read token is shown. A value CONTAINING it is refused, not only one equal to
// it, because a URL is masked in part ("rtmp://host/app/[redacted]"). No real
// stream key, passphrase, token or URL contains the bracketed word.

// credentialField is one credential-bearing field, named by its JSON path so
// the refusal points at the exact key a client would have to fix.
type credentialField struct {
	path  string
	value string
}

// redactionPlaceholderProblems reports each field whose value carries the
// redaction placeholder. The value itself is never echoed: a validation error
// lands in a 400 body and in the server log, and a partly masked URL still
// carries the parts that were not masked.
func redactionPlaceholderProblems(fields ...credentialField) []string {
	var probs []string
	for _, f := range fields {
		if strings.Contains(f.value, alerts.Mask) {
			probs = append(probs, fmt.Sprintf(
				"%s carries the redaction placeholder %q, which is what a read-only "+
					"view shows in place of the real value -- send the real value, or leave "+
					"the field as it is stored", f.path, alerts.Mask))
		}
	}
	return probs
}
