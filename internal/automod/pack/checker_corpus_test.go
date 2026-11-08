package pack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This file wires the corpus harness (corpus_test.go) to the real checker. The
// harness runs the same Checker the engine builds, with one injected difference:
// the reserved-range policy. Production rejects the RFC 5737 and RFC 3849
// documentation ranges as addresses (a chat message quoting one is harmless);
// the corpus may not contain real addresses, so it uses exactly those ranges
// and the harness runs the detector with Documentation false. Private and
// loopback ranges stay rejected under both policies.

func corpusNames(tb testing.TB) *NameSet {
	fail := func(err error) {
		if tb != nil {
			tb.Helper()
			tb.Fatal(err)
		}
		panic(err)
	}
	b, err := os.ReadFile(filepath.Join("testdata", "protected-names.json"))
	if err != nil {
		fail(err)
	}
	var f struct {
		Names []struct {
			Name string `json:"name"`
		} `json:"names"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		fail(err)
	}
	var names []string
	for _, n := range f.Names {
		names = append(names, n.Name)
	}
	return NewNameSet(names)
}

func corpusConfig(tb testing.TB) Config {
	return Config{
		Shape:    DefaultShapeLimits(),
		Reserved: ReservedPolicy{Documentation: false},
		Names:    corpusNames(tb),
	}
}

func hitsToFindings(hs []Hit) []corpusFinding {
	out := make([]corpusFinding, 0, len(hs))
	for _, h := range hs {
		out = append(out, corpusFinding{Group: h.Group, Hold: h.Hold})
	}
	return out
}

var corpusEpoch = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

func init() {
	// The harness is wired lazily: TestMain-free, so build on first use.
	var chk *Checker
	corpusUnderTest = func(l corpusLine, _ Forms) []corpusFinding {
		if chk == nil {
			c, err := New(corpusConfig(nil))
			if err != nil {
				panic(err)
			}
			chk = c
		}
		return hitsToFindings(chk.Inspect(Message{
			Platform: l.Platform, AuthorName: l.Author, Text: l.Text,
		}))
	}
	corpusSeqUnderTest = func(s corpusSeq) [][]corpusFinding {
		cfg := corpusConfig(nil)
		cfg.Raid, cfg.NearDup = NewRaid(RaidConfig{}), NewNearDup(NearDupConfig{})
		c, err := New(cfg)
		if err != nil {
			panic(err)
		}
		out := make([][]corpusFinding, len(s.Messages))
		for i, m := range s.Messages {
			if m.Role != "" {
				// Trusted() drops the broadcaster's and moderators' messages
				// before they reach any checker; the harness does the same.
				continue
			}
			out[i] = hitsToFindings(c.Observe(Message{
				Platform: m.Platform, AuthorID: m.Author, AuthorName: m.Author, Text: m.Text,
				At: corpusEpoch.Add(time.Duration(m.OffsetMs) * time.Millisecond),
			}))
		}
		return out
	}
}
