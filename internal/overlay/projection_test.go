package overlay

import (
	"reflect"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
)

// Every field of chat.Message and chat.Author is classified here as shown on an
// overlay or deliberately dropped. A field added to either struct fails this
// test until somebody decides which -- so identity, moderation or routing data
// cannot drift onto an OBS scene by default.
var messageFields = map[string]string{
	"ID":        "dropped: replaced by an opaque per-process display id",
	"Platform":  "shown: the platform filter and the platform mark need it",
	"Account":   "dropped: the operator's account ref",
	"Channel":   "dropped: not needed on screen",
	"Author":    "partly: see authorFields",
	"Text":      "shown",
	"Emotes":    "dropped: remote images are blocked by the CSP; the emote's text stays in Text",
	"At":        "shown",
	"Action":    "shown",
	"ReplyToID": "dropped: a platform message id",
	"ReplyTo":   "dropped: names a third party who did not write this line",
	"Echo":      "dropped: internal",
}

var authorFields = map[string]string{
	"ID":          "dropped: the viewer's platform user id",
	"Name":        "shown",
	"Color":       "shown",
	"Badges":      "shown",
	"Moderator":   "shown: a public badge",
	"Subscriber":  "shown: a public badge",
	"Broadcaster": "shown: a public badge",
}

func TestEveryChatFieldIsClassifiedForTheOverlay(t *testing.T) {
	check := func(name string, typ reflect.Type, known map[string]string) {
		seen := map[string]bool{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i).Name
			seen[f] = true
			if _, ok := known[f]; !ok {
				t.Errorf("%s.%s is not classified as shown or dropped for overlays", name, f)
			}
		}
		for f := range known {
			if !seen[f] {
				t.Errorf("%s.%s is classified but no longer exists", name, f)
			}
		}
	}
	check("chat.Message", reflect.TypeOf(chat.Message{}), messageFields)
	check("chat.Author", reflect.TypeOf(chat.Author{}), authorFields)
}

func TestTheProjectionCarriesNoDroppedValue(t *testing.T) {
	g := New(nopPub{})
	msg := chat.Message{
		ID: "PLATFORMID", Platform: "twitch", Account: "ACCOUNTREF", Channel: "CHANNEL",
		Author:    chat.Author{ID: "USERID", Name: "n"},
		Text:      "t",
		ReplyToID: "REPLYID", ReplyTo: "REPLYNAME",
	}
	got := reflect.ValueOf(g.project(msg))
	for i := 0; i < got.NumField(); i++ {
		if s, ok := got.Field(i).Interface().(string); ok {
			for _, bad := range []string{"PLATFORMID", "ACCOUNTREF", "CHANNEL", "USERID", "REPLYID", "REPLYNAME"} {
				if s == bad {
					t.Errorf("projection field %s carries %s", got.Type().Field(i).Name, bad)
				}
			}
		}
	}
}
