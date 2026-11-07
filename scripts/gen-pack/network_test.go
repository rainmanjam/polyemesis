//go:build genpacknet

package main

import (
	"bytes"
	"os"
	"testing"
)

// Needs the network, so it is built only with -tags genpacknet: it proves the committed table
// is what the pinned confusables.txt produces.
func TestSkeletonTableMatchesPinnedConfusables(t *testing.T) {
	p, err := loadPins("pins.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src, err := fetchConfusables(p.Unicode)
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := buildSkeleton(src, p.Unicode.Version)
	if err != nil {
		t.Fatal(err)
	}
	have, err := os.ReadFile("../../internal/automod/pack/skeleton_table.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(have, want) {
		t.Fatal("skeleton_table.go differs from the generator output: run `go generate ./internal/automod/pack`")
	}
}
