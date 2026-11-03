package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func tikTokStub(t *testing.T, h http.HandlerFunc) *TikTok {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewTikTok(WithBaseURL(srv.URL))
}

func TestTikTokAuthURLUsesClientKeyAndCommaScopesAndNoPKCE(t *testing.T) {
	tk := NewTikTok()
	if tk.PKCE() {
		t.Fatal("PKCE() is true; the web flow documents no code_challenge")
	}
	raw := tk.AuthURL("key-1", "https://box.test/api/v1/oauth/tiktok/callback", "st", "a-challenge")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "www.tiktok.com" || u.Path != "/v2/auth/authorize/" {
		t.Fatalf("authorize endpoint = %s%s", u.Host, u.Path)
	}
	q := u.Query()
	if q.Get("client_key") != "key-1" || q.Get("response_type") != "code" || q.Get("state") != "st" {
		t.Fatalf("query = %v", q)
	}
	if q.Get("scope") != "user.info.basic,video.publish" {
		t.Fatalf("scope = %q", q.Get("scope"))
	}
	if q.Has("code_challenge") || q.Has("client_id") {
		t.Fatalf("query leaks %v", q)
	}
}

func TestTikTokExchangeAndRefreshPostTheDocumentedForm(t *testing.T) {
	var forms []url.Values
	tk := tikTokStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/oauth/token/" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		forms = append(forms, r.PostForm)
		fmt.Fprint(w, `{"open_id":"o1","access_token":"act-placeholder","refresh_token":"rft-placeholder","expires_in":86400,"scope":"user.info.basic,video.publish","token_type":"Bearer"}`)
	})
	tok, err := tk.Exchange(context.Background(), "key-1", "secret-1", "https://box.test/cb", "the-code", "")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "act-placeholder" || tok.RefreshToken != "rft-placeholder" || tok.Scopes != "user.info.basic,video.publish" || tok.ExpiresAt.IsZero() {
		t.Fatalf("token = %v", tok)
	}
	if _, err := tk.Refresh(context.Background(), "key-1", "secret-1", "rft-placeholder"); err != nil {
		t.Fatal(err)
	}
	if len(forms) != 2 {
		t.Fatalf("requests = %d", len(forms))
	}
	if f := forms[0]; f.Get("client_key") != "key-1" || f.Get("grant_type") != "authorization_code" || f.Get("code") != "the-code" || f.Get("redirect_uri") != "https://box.test/cb" {
		t.Errorf("exchange form = %v", f)
	}
	if f := forms[1]; f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != "rft-placeholder" {
		t.Errorf("refresh form = %v", f)
	}
}

func TestTikTokAccountReadsTheOpenIDAndName(t *testing.T) {
	tk := tikTokStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer act-placeholder" || !strings.HasPrefix(r.URL.Path, "/v2/user/info/") {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"data":{"user":{"open_id":"o1","display_name":"Creator"}},"error":{"code":"ok","message":""}}`)
	})
	a, err := tk.Account(context.Background(), "key", "act-placeholder")
	if err != nil || a.Ref != "o1" || a.Name != "Creator" {
		t.Fatalf("account = %+v, %v", a, err)
	}
}

func TestTikTokAccountSurfacesAPlatformErrorAndAnEmptyIdentity(t *testing.T) {
	bad := tikTokStub(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{},"error":{"code":"scope_not_authorized","message":"nope"}}`)
	})
	if _, err := bad.Account(context.Background(), "key", "t"); err == nil || !strings.Contains(err.Error(), "scope_not_authorized") {
		t.Fatalf("err = %v", err)
	}
	empty := tikTokStub(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"user":{}},"error":{"code":"ok"}}`)
	})
	if _, err := empty.Account(context.Background(), "key", "t"); err == nil {
		t.Fatal("an empty identity was accepted")
	}
}

func TestTikTokNeverOffersAStreamKey(t *testing.T) {
	_, err := NewTikTok().Ingest(context.Background(), "k", "t")
	if !errors.Is(err, ErrNoStreamKeyAPI) {
		t.Fatalf("Ingest = %v, want ErrNoStreamKeyAPI", err)
	}
	if NewTikTok().ManualKeyReason() == "" {
		t.Fatal("no manual key reason")
	}
	if _, ok := ManualKeyFor(db.PlatformTikTok); !ok {
		t.Fatal("TikTok is not advertised as manual-key")
	}
}

func TestTikTokIsRegisteredAndNeedsAReconnectWithoutThePublishScope(t *testing.T) {
	if _, err := Get(db.PlatformTikTok); err != nil {
		t.Fatal(err)
	}
	r := AccountNeedsReconnect(db.PlatformAccount{Platform: db.PlatformTikTok, Scopes: "user.info.basic"})
	if !r.Needed {
		t.Fatalf("a TikTok grant without video.publish was judged fine: %+v", r)
	}
	if AccountNeedsReconnect(db.PlatformAccount{Platform: db.PlatformTikTok, Scopes: "user.info.basic,video.publish"}).Needed {
		t.Fatal("a complete grant was flagged")
	}
}
