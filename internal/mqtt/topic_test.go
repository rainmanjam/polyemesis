package mqtt

import (
	"errors"
	"strings"
	"testing"
)

func newTopics(t *testing.T, prefix, instance string) *Topics {
	t.Helper()
	tp, err := NewTopics(prefix, instance)
	if err != nil {
		t.Fatalf("NewTopics(%q, %q): %v", prefix, instance, err)
	}
	return tp
}

func TestTopicsBuildTheDocumentedTree(t *testing.T) {
	tp := newTopics(t, "", "studio")
	cases := []struct{ got, want string }{
		{tp.Status(), "polyemesis/studio/status"},
		{tp.State(), "polyemesis/studio/state"},
		{tp.Source("cam1"), "polyemesis/studio/source/cam1/state"},
		{tp.Dest("cam1", "twitch"), "polyemesis/studio/source/cam1/dest/twitch/state"},
		{tp.Rendition("cam1", "720p"), "polyemesis/studio/source/cam1/rendition/720p/state"},
		{tp.SourceRoot("cam1"), "polyemesis/studio/source/cam1"},
		{tp.All(), "polyemesis/studio/#"},
		{tp.Discovery(), "homeassistant/device/studio/config"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("topic = %q, want %q", c.got, c.want)
		}
	}
}

// Every published topic must itself be publishable. A wildcard is legal in a
// subscription filter and illegal in a topic name, and that distinction is the
// one most easily got wrong -- All() is deliberately the only exception.
func TestEveryPublishedTopicIsAValidTopicName(t *testing.T) {
	tp := newTopics(t, "home/av", "studio one")
	published := []string{
		tp.Status(), tp.State(), tp.Source("cam1"),
		tp.Dest("cam1", "twitch"), tp.Rendition("cam1", "720p"), tp.Discovery(),
	}
	for _, topic := range published {
		if err := Valid(topic); err != nil {
			t.Errorf("published topic %q is not publishable: %v", topic, err)
		}
	}
	// The wildcard is a filter, not a topic name, and must NOT pass. Without
	// this the check above would be satisfied by a Valid that returns nil for
	// everything.
	if err := Valid(tp.All()); err == nil {
		t.Error("Valid accepted a wildcard as a topic name; a publish to it would be rejected by the broker")
	}
}

// A prefix beginning with $ is refused rather than escaped: the broker would
// accept the publish, and a subscriber using '#' -- which is what anybody
// debugging reaches for -- is specified never to receive it.
func TestDollarPrefixIsRefused(t *testing.T) {
	if _, err := NewTopics("$SYS", "studio"); !errors.Is(err, ErrDollarPrefix) {
		t.Errorf("NewTopics with a $ prefix returned %v, want ErrDollarPrefix", err)
	}
	// A $ anywhere else is harmless and must still be accepted, or the check is
	// a substring match pretending to be a prefix check.
	if _, err := NewTopics("home/a$b", "studio"); err != nil {
		t.Errorf("NewTopics rejected a mid-string $: %v", err)
	}
}

func TestWildcardInAPrefixIsRefused(t *testing.T) {
	for _, p := range []string{"home/+", "home/#", "home/\x00bad"} {
		if _, err := NewTopics(p, "studio"); err == nil {
			t.Errorf("NewTopics(%q) was accepted; every publish beneath it would fail", p)
		}
	}
}

// The prefix is validated, not slugged. An operator who writes `home/av` means
// two levels, and slugging would turn it into `home-av`, silently relocating
// their whole tree to somewhere their subscriptions do not look.
func TestPrefixKeepsItsSeparators(t *testing.T) {
	tp := newTopics(t, "home/av/", "studio")
	if got := tp.State(); got != "home/av/studio/state" {
		t.Errorf("State() = %q, want the prefix preserved as two levels", got)
	}
}

// An empty segment produces `//`, which MQTT treats as a real empty level and
// therefore a different topic from the intended one -- matching no filter the
// operator would write, with nothing reporting it.
func TestNoTopicEverContainsAnEmptyLevel(t *testing.T) {
	tp := newTopics(t, "", "")
	topics := []string{
		tp.Status(), tp.State(), tp.Source(Slug("")), tp.Dest(Slug(""), Slug("")),
		tp.Rendition(Slug(""), Slug("!!!")), tp.Discovery(),
	}
	for _, topic := range topics {
		if strings.Contains(topic, "//") || strings.HasSuffix(topic, "/") {
			t.Errorf("topic %q contains an empty level", topic)
		}
	}
}

func TestValidRejectsAnOversizedTopic(t *testing.T) {
	if err := Valid(strings.Repeat("a", maxTopicBytes+1)); err == nil {
		t.Error("Valid accepted a topic over the MQTT length limit")
	}
	if err := Valid(""); err == nil {
		t.Error("Valid accepted an empty topic")
	}
}
