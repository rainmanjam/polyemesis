package pack

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// benchMessage is the shape the cost target was set against: about 100
// characters with fullwidth characters, a link and mentions.
const benchMessage = "ＬＥＴＳ ＧＯ @streamer_one and @mod_two, watch https://example.com/clip?id=42 th1s w4s s0 gooood lol"

// benchIndex stands in for the checker's term index: a map of every pack term
// (padded with n-gram-shaped keys to 2,000) so the lookup cost is realistic.
func benchIndex(tb testing.TB) map[string]struct{} {
	tb.Helper()
	p, err := Default()
	if err != nil {
		tb.Fatal(err)
	}
	m := make(map[string]struct{}, 2000)
	for _, e := range p.Entries {
		m[e.Term] = struct{}{}
	}
	for i := 0; len(m) < 2000; i++ {
		m["padding"+strings.Repeat("x", i%7)+" "+strings.Repeat("y", i/7+1)] = struct{}{}
	}
	return m
}

// lookup builds 1- to 4-gram keys in a reused buffer, which is how the checker
// avoids the string concatenation that dominated the first measurement.
func lookup(idx map[string]struct{}, toks []string, buf []byte) ([]byte, int) {
	hits := 0
	for i := range toks {
		buf = buf[:0]
		for n := 0; n < 4 && i+n < len(toks); n++ {
			if n > 0 {
				buf = append(buf, ' ')
			}
			buf = append(buf, toks[i+n]...)
			if _, ok := idx[string(buf)]; ok {
				hits++
			}
		}
	}
	return buf, hits
}

func BenchmarkNFKC(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = NFKC(benchMessage)
	}
}

// BenchmarkPackForm is the whole per-message cost on the adapter goroutine:
// every shared form plus the term lookup.
func BenchmarkPackForm(b *testing.B) {
	idx := benchIndex(b)
	buf := make([]byte, 0, 128)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f := NewForms(benchMessage)
		buf, _ = lookup(idx, f.Tokens, buf)
	}
}

func TestBenchMessageShape(t *testing.T) {
	if n := utf8.RuneCountInString(benchMessage); n < 90 || n > 110 {
		t.Fatalf("benchmark message is %d characters; the cost target assumes about 100", n)
	}
	f := NewForms(benchMessage)
	if !strings.Contains(f.Pack, "lets go") || !strings.Contains(f.Pack, "this was so good") {
		t.Fatalf("benchmark message no longer exercises the folds: %q", f.Pack)
	}
}
