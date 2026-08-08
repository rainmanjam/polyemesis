//go:build ignore

// Seeds three differently-routed FILE destinations straight into the database,
// so a server with no API credentials can still be measured for per-destination
// routing. Uses the project's own db package so the profile JSON is exactly
// what the engine expects rather than hand-rolled.
package main

import (
	"fmt"
	"os"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

func profileFor(on ...int) routing.Profile {
	p := routing.DefaultProfile()
	want := map[int]bool{}
	for _, t := range on {
		want[t] = true
	}
	for i := range p.Tracks {
		p.Tracks[i].Enabled = want[p.Tracks[i].Track]
		p.Tracks[i].Gain = 1.0
	}
	p.Normalize = "off"
	return p
}

func main() {
	store, err := db.Open(os.Args[1])
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	defer store.Close()

	srcs, err := store.ListSources()
	if err != nil || len(srcs) == 0 {
		fmt.Println("no sources:", err)
		os.Exit(1)
	}
	sid := srcs[0].ID

	for _, w := range []struct {
		name string
		file string
		on   []int
	}{
		{"A-track1", "ovhA.mkv", []int{0}},
		{"B-track2", "ovhB.mkv", []int{1}},
		{"C-all", "ovhC.mkv", []int{0, 1, 2}},
	} {
		d := &db.Destination{
			Name: w.name, Kind: db.DestFile, URL: w.file,
			Enabled: true, AudioBitrate: 160,
			Profile: profileFor(w.on...), SourceID: &sid,
		}
		if _, err := store.CreateDestination(d); err != nil {
			fmt.Printf("create %s: %v\n", w.name, err)
			os.Exit(1)
		}
		fmt.Printf("created %s -> %s tracks=%v\n", w.name, w.file, w.on)
	}
	fmt.Println("SEED_OK")
}
