package objstore

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Review #14: the request target on the wire is origin-form -- the escaped
// path the signature covers -- over HTTP/1.1 and over HTTP/2.
func TestTheRequestTargetIsOriginFormOverHTTP1AndHTTP2(t *testing.T) {
	type seen struct{ uri, proto, host string }
	for _, h2 := range []bool{false, true} {
		var got seen
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = seen{r.RequestURI, r.Proto, r.Host}
			w.WriteHeader(200)
		}))
		if h2 {
			srv.EnableHTTP2 = true
			srv.StartTLS()
		} else {
			srv.Start()
		}
		hc := srv.Client()
		if h2 {
			hc.Transport.(*http.Transport).ForceAttemptHTTP2 = true
		}
		c, err := New(Config{Endpoint: srv.URL, Bucket: "media", PathStyle: true, AccessKeyID: fakeKeyID,
			SecretAccessKey: fakeSecret, HTTPClient: hc})
		if err != nil {
			t.Fatal(err)
		}
		body := []byte("x")
		sh, mb := digests(body)
		if _, err := c.Put(context.Background(), "rec/a b+c=d~e.mkv", bytes.NewReader(body), 1, sh, mb, nil); err != nil {
			t.Fatal(err)
		}
		srv.Close()
		wantProto := "HTTP/1.1"
		if h2 {
			wantProto = "HTTP/2.0"
		}
		if got.proto != wantProto {
			t.Fatalf("proto %q, want %q", got.proto, wantProto)
		}
		if got.uri != "/media/rec/a%20b%2Bc%3Dd~e.mkv" {
			t.Fatalf("%s request target %q, want origin-form /media/rec/a%%20b%%2Bc%%3Dd~e.mkv", got.proto, got.uri)
		}
	}
}
