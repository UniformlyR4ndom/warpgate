package main

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestServerCloseStopsSweeper(t *testing.T) {
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv, err := NewServer(Config{Root: t.TempDir(), UlRoot: t.TempDir(), NoAuth: true}, db)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	select {
	case <-srv.sweepDone:
	default:
		t.Fatal("sweeper still running after Close")
	}
	srv.Close()
}

func TestNewServerFailureClosesRoots(t *testing.T) {
	fds := func() int {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Skip("needs /proc/self/fd")
		}
		return len(entries)
	}
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := Config{Root: t.TempDir(), UlRoot: t.TempDir(), NoAuth: true, Cert: "c", CertKey: "k", ClientFP: "zz"}
	before := fds()
	for i := 0; i < 5; i++ {
		if srv, err := NewServer(cfg, db); err == nil || srv != nil {
			t.Fatalf("NewServer = %v, %v; want an error", srv, err)
		}
	}
	if after := fds(); after != before {
		t.Errorf("open file descriptors went from %d to %d", before, after)
	}
}

func TestListenAndServeShutsDown(t *testing.T) {
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(t, Config{Root: t.TempDir(), Addr: "127.0.0.1:0", NoAuth: true}, db)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ListenAndServe = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}
}
