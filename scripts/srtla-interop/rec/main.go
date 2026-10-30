// Command rec is the receiving end of the SRTLA interop harness
// (scripts/srtla-interop/run.sh): internal/srtla in front of a bare gosrt
// listener that counts bytes, with nothing else of polyemesis involved.
//
// The reference sender, BELABOX's srtla_send, is run against it in the same
// container. rec exits 0 once the listener has received at least -min-bytes
// under the expected streamid AND the receiver reports at least -links links
// that each carried at least -min-link-packets datagrams -- the evidence that
// a real bonded sender's traffic was merged from several paths. It prints one
// JSON line with what it saw either way, and exits 1 on timeout.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	srt "github.com/datarhei/gosrt"

	"github.com/rainmanjam/polyemesis/internal/srtla"
)

type result struct {
	OK        bool               `json:"ok"`
	Reason    string             `json:"reason,omitempty"`
	StreamID  string             `json:"streamId"`
	Bytes     int64              `json:"bytes"`
	Groups    []srtla.GroupStats `json:"groups"`
	ElapsedMS int64              `json:"elapsedMs"`
}

func main() {
	listen := flag.String("listen", ":5000", "SRTLA listen address")
	upstream := flag.String("upstream", "127.0.0.1:6000", "SRT listener address")
	streamID := flag.String("streamid", "", "streamid the publisher must present")
	minBytes := flag.Int64("min-bytes", 1<<20, "bytes the SRT listener must receive")
	minLinks := flag.Int("links", 2, "links that must each carry traffic")
	minLinkPackets := flag.Uint64("min-link-packets", 50, "datagrams each of those links must carry")
	timeout := flag.Duration("timeout", 90*time.Second, "give up after")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := srt.DefaultConfig()
	ln, err := srt.Listen("srt", *upstream, cfg)
	if err != nil {
		fail(log, "srt listen: %v", err)
	}
	defer ln.Close()

	var total atomic.Int64
	var seenID atomic.Value
	seenID.Store("")
	go func() {
		for {
			req, err := ln.Accept2()
			if err != nil {
				return
			}
			id := req.StreamId()
			seenID.Store(id)
			if *streamID != "" && id != *streamID {
				log.Warn("publisher presented an unexpected streamid; rejecting")
				req.Reject(srt.REJ_PEER)
				continue
			}
			conn, err := req.Accept()
			if err != nil {
				log.Warn("srt accept failed", "err", err)
				continue
			}
			log.Info("srt publisher connected", "peer", conn.RemoteAddr().String())
			go func() {
				defer conn.Close()
				buf := make([]byte, 2048)
				for {
					n, err := conn.Read(buf)
					total.Add(int64(n))
					if err != nil {
						if !errors.Is(err, io.EOF) {
							log.Info("srt read ended", "err", err)
						}
						return
					}
				}
			}()
		}
	}()

	rx := srtla.New(log, srtla.Config{Listen: *listen, Upstream: *upstream})
	if err := rx.Start(); err != nil {
		fail(log, "srtla start: %v", err)
	}
	defer rx.Stop()

	start := time.Now()
	deadline := start.Add(*timeout)
	for {
		time.Sleep(500 * time.Millisecond)
		groups := rx.Stats()
		busy := 0
		for _, g := range groups {
			n := 0
			for _, l := range g.Links {
				if l.Packets >= *minLinkPackets {
					n++
				}
			}
			busy = max(busy, n)
		}
		res := result{
			StreamID:  seenID.Load().(string),
			Bytes:     total.Load(),
			Groups:    groups,
			ElapsedMS: time.Since(start).Milliseconds(),
		}
		switch {
		case res.Bytes >= *minBytes && busy >= *minLinks && (*streamID == "" || res.StreamID == *streamID):
			res.OK = true
			emit(res)
			return
		case time.Now().After(deadline):
			res.Reason = fmt.Sprintf("timeout: bytes=%d/%d, links with >=%d packets=%d/%d",
				res.Bytes, *minBytes, *minLinkPackets, busy, *minLinks)
			emit(res)
			rx.Stop()
			os.Exit(1)
		}
	}
}

func emit(r result) {
	b, _ := json.Marshal(r)
	fmt.Println("RESULT " + string(b))
}

func fail(log *slog.Logger, format string, args ...any) {
	log.Error(fmt.Sprintf(format, args...))
	os.Exit(2)
}
