package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// ============================================================================
// REMOVING AN APP'S CREDENTIALS WHILE ACCOUNTS STILL USE IT.
//
// Deleting a platform's developer credentials deletes only the app. The
// accounts connected through it, and their sealed tokens, stay: tokenFor hands
// out any token that has not expired without reading the credentials, so each
// account keeps working until its access token expires -- and one whose token
// came back with no expiry keeps working indefinitely. Only then does it fail,
// unable to refresh. The SPA said otherwise: its comment and test claimed the
// server refused while accounts used the app, and no server code ever did.
//
// So this is the account_in_use pattern from handleDeleteAccount, for the
// credentials. An UNCONFIRMED removal while accounts exist is refused with a
// 409 that names them; the same request with {"confirm": true} goes ahead
// exactly as before. With no accounts the bare DELETE answers as it always
// has. It never deletes an account -- that is handleDeleteAccount's, with its
// own destination guard -- so it cannot be used to go around that guard.
// ============================================================================

// codeCredsInUse is the branch key for the refusal. A client matches on it,
// not on the English, and answers it by re-sending with {"confirm": true}.
const codeCredsInUse = "creds_in_use"

// credsDeleteRefusal is the 409 body.
type credsDeleteRefusal struct {
	Error    string           `json:"error"`
	Code     string           `json:"code"`
	Accounts []credsInUseItem `json:"accounts"`
}

type credsInUseItem struct {
	ID          int64  `json:"id"`
	AccountName string `json:"accountName"`
}

// accountsUsingApp lists the accounts connected through this platform's app.
func (s *Server) accountsUsingApp(platform db.Platform) ([]credsInUseItem, error) {
	all, err := s.store.ListPlatformAccounts()
	if err != nil {
		return nil, err
	}
	var out []credsInUseItem
	for _, a := range all {
		if a.Platform == platform {
			out = append(out, credsInUseItem{ID: a.ID, AccountName: a.AccountName})
		}
	}
	return out, nil
}

// refuseCredsInUse answers the request itself, and reports true, when the
// removal must not go ahead: the body was unreadable, the accounts could not
// be counted, or accounts use the app and the caller did not confirm.
func (s *Server) refuseCredsInUse(w http.ResponseWriter, r *http.Request, platform db.Platform) bool {
	confirmed, ok := deleteConfirmed(w, r)
	if !ok {
		return true
	}
	using, err := s.accountsUsingApp(platform)
	if err != nil {
		writeStoreError(w, err)
		return true
	}
	if len(using) == 0 || confirmed {
		return false
	}
	writeJSON(w, http.StatusConflict, credsDeleteRefusal{
		Error:    credsInUseMessage(platform, using),
		Code:     codeCredsInUse,
		Accounts: using,
	})
	return true
}

func credsInUseMessage(platform db.Platform, using []credsInUseItem) string {
	const shown = 3
	names := make([]string, 0, shown)
	for i, a := range using {
		if i == shown {
			names = append(names, fmt.Sprintf("and %d more", len(using)-shown))
			break
		}
		names = append(names, a.AccountName)
	}
	return fmt.Sprintf("%s still connected through this %s app: %s. Removing the app's credentials does "+
		"not disconnect %s: %s keep working until %s access token expires, then cannot refresh and must "+
		"be reconnected. Disconnect %s first, or send this request again with {\"confirm\": true} to "+
		"remove the credentials anyway.",
		plural(len(using), "account is", "accounts are"), platformTitle(platform), strings.Join(names, ", "),
		wordFor(len(using), "it", "them"), wordFor(len(using), "it will", "they will"),
		wordFor(len(using), "its", "each one's"), wordFor(len(using), "it", "them"))
}
