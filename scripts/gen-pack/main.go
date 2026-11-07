// Command gen-pack rebuilds the automod pack from its pinned sources.
//
//	go run ./scripts/gen-pack            # rewrite internal/automod/pack/en-v1.json
//	go run ./scripts/gen-pack -check     # fail if the committed pack differs
//	go run ./scripts/gen-pack -fetch     # refresh scripts/gen-pack/sources from the pins
//	go run ./scripts/gen-pack -skeleton  # rewrite internal/automod/pack/skeleton_table.go
//
// The generated JSON is committed, so a change to the pack, whether from a
// moved pin or an edit to overrides.yaml, shows up in review as a term diff.
// Generation itself reads only the committed snapshots under sources/ and never
// touches the network; -fetch and -skeleton are the only network paths, and both
// verify a SHA-256 before using what they download.
//
// Output is counts only. The pack contains slurs by necessity, and a generator
// that printed terms would copy them into every CI log.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	root := flag.String("root", ".", "repository root")
	check := flag.Bool("check", false, "fail if the committed pack differs from the generator output")
	fetch := flag.Bool("fetch", false, "download the pinned sources into sources/ and verify their hashes")
	skeleton := flag.Bool("skeleton", false, "regenerate skeleton_table.go from the pinned confusables.txt")
	flag.Parse()

	if err := run(*root, *check, *fetch, *skeleton); err != nil {
		fmt.Fprintln(os.Stderr, "gen-pack:", err)
		os.Exit(1)
	}
}

func run(root string, check, fetch, skeleton bool) error {
	genDir := filepath.Join(root, "scripts", "gen-pack")
	packDir := filepath.Join(root, "internal", "automod", "pack")

	pins, err := loadPins(filepath.Join(genDir, "pins.yaml"))
	if err != nil {
		return err
	}

	if fetch {
		if err := fetchSources(pins, filepath.Join(genDir, "sources")); err != nil {
			return err
		}
		fmt.Println("sources match their pins")
		return nil
	}

	if skeleton {
		src, err := fetchConfusables(pins.Unicode)
		if err != nil {
			return err
		}
		out, n, err := buildSkeleton(src, pins.Unicode.Version)
		if err != nil {
			return err
		}
		fmt.Printf("skeleton: %d mappings (Unicode %s)\n", n, pins.Unicode.Version)
		return os.WriteFile(filepath.Join(packDir, "skeleton_table.go"), out, 0o644)
	}

	out, stats, err := generate(genDir)
	if err != nil {
		return err
	}
	fmt.Print(stats.String())

	path := filepath.Join(packDir, "en-v1.json")
	if check {
		have, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(have, out) {
			return fmt.Errorf("%s is out of date: run `go run ./scripts/gen-pack` and commit the result", path)
		}
		fmt.Println("pack is up to date")
		return nil
	}
	return os.WriteFile(path, out, 0o644)
}
