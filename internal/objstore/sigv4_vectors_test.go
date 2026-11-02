package objstore

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Review #24: known-answer SigV4 vectors, computed independently by botocore
// (S3SigV4Auth, the AWS SDK's own signer) for the low-entropy test credential,
// so a regression in canonicalisation -- path escaping, unicode, the query --
// fails here and not only against a live server.
func TestSignaturesMatchBotocoreForTheSameRequests(t *testing.T) {
	at := time.Date(2026, 11, 2, 4, 18, 58, 0, time.UTC)
	cases := []struct {
		method, url, canon, want string
	}{
		{"GET", "http://h.test/media/rec/a%20b%2Bc.mkv", "/media/rec/a%20b%2Bc.mkv",
			"36ed54b2835716ca39561a9044d0c289ab5b8229ab0aa82bc8024e2a22a0fbb4"},
		{"GET", "http://h.test/media/rec/%C3%A9t%C3%A9%20%E2%82%AC.mkv", "/media/rec/%C3%A9t%C3%A9%20%E2%82%AC.mkv",
			"9ba1b65d42b685cc61750309e235900d315397a1e590ef0e301983750ba3f3fe"},
		{"DELETE", "http://h.test/media/k?uploadId=abc%2Fdef", "/media/k",
			"e13c8d7c543ea49ec95d86689ea21d665c002858bd258d8cd01d011ef60e6f58"},
	}
	c, err := New(Config{Endpoint: "http://h.test", Bucket: "media", PathStyle: true, Region: "us-east-1",
		AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret})
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return at }
	for _, tc := range cases {
		req, _ := http.NewRequest(tc.method, tc.url, nil)
		c.sign(req, tc.canon, emptySHA256)
		auth := req.Header.Get("Authorization")
		if got := auth[strings.Index(auth, "Signature=")+10:]; got != tc.want {
			t.Errorf("%s %s: signature %s, botocore %s", tc.method, tc.url, got, tc.want)
		}
	}
	// And the unicode key is escaped to exactly that canonical path.
	if _, p := c.objectURL("rec/été €.mkv"); p != "/media/rec/%C3%A9t%C3%A9%20%E2%82%AC.mkv" {
		t.Fatalf("canonical path %q", p)
	}
}
