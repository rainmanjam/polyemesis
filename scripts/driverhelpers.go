//go:build ignore

// Package main -- the HTTP session four acceptance drivers had four copies of.
//
// NOT A PACKAGE, AND IT CANNOT BE ONE. These drivers are invoked as
//
//	go run "$SCRIPTS/acceptance_audio_driver.go" "$SCRIPTS/driverhelpers.go" "$PORT" ...
//
// from a shell that has already cd'd into $WORK under /tmp, outside any module.
// `go run` resolves a module IMPORT against the current directory's go.mod, so
// importing scripts/internal/driverlib from there fails with "go.mod file not
// found" -- acceptance-ladder.sh is the only suite that imports it and the only
// one that wraps its invocation in `cd "$ROOT"`, which its comment calls
// "required rather than tidy".
//
// Naming the file on the command line sidesteps that entirely. cmd/go: "if the
// package list is a list of .go files from a single directory, the command is
// applied to a single synthesized package made up of exactly those files,
// ignoring any build constraints in those files". So the //go:build ignore
// above is honoured by `go build ./...` (which must keep skipping these) and
// deliberately ignored when the file is named -- specified behaviour, not a
// quirk, and covered by the Go 1 compatibility promise.
//
// THE FILES MUST BE IN ONE DIRECTORY, which is why this lives beside them.
//
// WHAT IS HERE AND WHAT IS NOT. Only functions that were byte-identical across
// acceptance_driver.go, acceptance_audio_driver.go, acceptance_encoders_driver.go
// and acceptance_renditions_driver.go.
//
// `die` IS DELIBERATELY ABSENT, and that is the important line in this comment.
// It looks identical and is not: acceptance_encoders_driver.go and
// acceptance_renditions_driver.go use a facts-PRESERVING variant that flushes
// results before exiting, and acceptance_mqtt_driver.go writes to stderr because
// its stdout is a value channel. Consolidating those would silently discard
// results on the failure path, in the suites whose entire output is a facts
// file. `waitUp` is shared only among these four for the same reason -- across
// the wider set it differs in health endpoint, budget (15-90s), poll interval,
// and whether HTTP 200 is required.
//
// postprod shares `call` and `grabCSRF` but has its own `waitUp` and `get`, so
// it keeps all four rather than taking a mixture.
//
// ON THE DUPLICATION THIS DOES NOT REMOVE. SonarCloud reports the acceptance
// drivers as the project's largest duplicated block, and consolidating them is
// what produced this file and pullsynthhelpers.go beside it. Absolute
// duplication fell from 1141 lines to 744; the gate's DENSITY went up, because
// it measures duplication in new code and its denominator is the lines a pull
// request touches -- so a refactor adds its own lines to the denominator while
// the untouched drivers keep their duplication in the numerator.
//
// The remainder is a long tail across six drivers plus smoketest.go duplicating
// itself, and chasing it would mean merging ten harnesses into one configurable
// library. These are test harnesses whose near-identical shape is deliberate:
// each suite is meant to be readable start to finish without chasing a shared
// abstraction. sonar.cpd.exclusions is set to scripts/** project-side instead.

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

func waitUp() {
	for i := 0; i < 60; i++ {
		if r, err := client.Get(base + "/health"); err == nil {
			r.Body.Close()
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	die("server never came up")
}

func grabCSRF() {
	req, _ := http.NewRequest("GET", base+"/health", nil)
	for _, c := range client.Jar.Cookies(req.URL) {
		if c.Name == "polyemesis_csrf" {
			csrf = c.Value
		}
	}
	if csrf == "" {
		die("no CSRF cookie issued")
	}
}

func call(method, path string, body any) map[string]any {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := client.Do(req)
	if err != nil {
		die("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		die("%s %s -> %d: %s", method, path, resp.StatusCode, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func get(path string) map[string]any {
	resp, err := client.Get(base + path)
	if err != nil {
		die("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		die("GET %s -> %d: %s", path, resp.StatusCode, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}
