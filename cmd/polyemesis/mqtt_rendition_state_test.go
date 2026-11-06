package main

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/mqtt"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// A rendition's row carries its encode settings, and running and bitrate only
// from a process: one with no consumers has none and is not running.
func TestMQTTRenditionState(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	rd := engine.RenditionStatus{
		ID: 7, Name: "720p Main", Width: 1280, Height: 720, FPS: 30,
		Codec: "h264", Encoder: db.VideoEncoder("x264"), Consumers: 2, Error: "boom",
	}

	idle := mqttRenditionState(rd, now)
	want := mqtt.RenditionState{
		ID: 7, Name: "720p Main", Slug: mqtt.Slug("720p Main"),
		Consumers: 2, Width: 1280, Height: 720, FPS: 30, Codec: "h264",
		Encoder: "x264", Error: "boom", At: now,
	}
	if idle != want {
		t.Fatalf("no process:\n got %+v\nwant %+v", idle, want)
	}

	rd.Process = &supervisor.Status{State: supervisor.StateRunning, Progress: ffmpeg.Progress{BitrateKbps: 2500}}
	running := mqttRenditionState(rd, now)
	if !running.Running || running.BitrateKbps != 2500 {
		t.Fatalf("running process: got running=%v bitrate=%v", running.Running, running.BitrateKbps)
	}
}
