package main

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestParseFlags(t *testing.T) {
	o, err := parseFlags([]string{"-addr", "127.0.0.1:9", "-latency", "5ms", "-jev-latency", "7ms", "-chunk-delay", "1ms",
		"-fault", "529@1", "-snapshot", "typesafe/jev-1.13-20261015", "-default-answer", "hi"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	c := o.cfg
	if o.addr != "127.0.0.1:9" || c.Latency != 5*time.Millisecond || c.JevLatency != 7*time.Millisecond || c.ChunkDelay != time.Millisecond ||
		c.Fault != "529@1" || c.Snapshot != "typesafe/jev-1.13-20261015" || c.DefaultAnswer != "hi" {
		t.Errorf("options %+v", o)
	}
	if o, _ := parseFlags(nil, io.Discard); o.addr != ":8090" || o.cfg.ChunkDelay != 20*time.Millisecond {
		t.Errorf("defaults %+v", o)
	}
	if _, err := parseFlags([]string{"extra"}, io.Discard); err == nil {
		t.Error("positional arguments are refused")
	}
	if _, err := parseFlags([]string{"-nope"}, io.Discard); err == nil {
		t.Error("unknown flags are refused")
	}
}

func TestRun(t *testing.T) {
	if err := run(context.Background(), []string{"-fault", "teapot"}, io.Discard); err == nil {
		t.Error("an unknown fault is refused at startup")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"-addr", "127.0.0.1:0"}, io.Discard) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("clean shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop")
	}
	if err := run(context.Background(), []string{"-h"}, io.Discard); err != nil {
		t.Errorf("-h: %v", err)
	}
}
