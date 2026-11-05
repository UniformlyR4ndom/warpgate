package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

type clipPollResult struct {
	rec     *httptest.ResponseRecorder
	elapsed time.Duration
}

func startClipPoll(s *Server, ctx context.Context, query string) <-chan clipPollResult {
	done := make(chan clipPollResult, 1)
	go func() {
		start := time.Now()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/clip/state?"+query, nil).WithContext(ctx)
		s.handleClipState(rec, req)
		done <- clipPollResult{rec, time.Since(start)}
	}()
	return done
}

func awaitClipPoll(t *testing.T, done <-chan clipPollResult) clipPollResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("clip/state request still held")
		return clipPollResult{}
	}
}

func decodeClipPoll(t *testing.T, r clipPollResult) clipStateResponse {
	t.Helper()
	if r.rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", r.rec.Code, r.rec.Body.String())
	}
	var st clipStateResponse
	if err := json.Unmarshal(r.rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestClipStateWaitReturnsAtOnceWhenNotCurrent(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{NoAuth: true, clipPollWait: time.Minute})
	mustInsert(t, s.db, "a", "one")

	for _, q := range []string{"since=1", "since=0&wait", "since=5&wait"} {
		r := awaitClipPoll(t, startClipPoll(s, context.Background(), q))
		if r.elapsed > time.Second {
			t.Errorf("%s: held for %v", q, r.elapsed)
		}
		decodeClipPoll(t, r)
	}
}

func TestClipStateWaitWakesOnChange(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{NoAuth: true, clipPollWait: time.Minute})
	mustInsert(t, s.db, "a", "one")
	rev, _, _, _, _ := s.db.ClipState(0)

	done := startClipPoll(s, context.Background(), "since="+strconv.FormatInt(rev, 10)+"&wait")
	time.Sleep(50 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("returned before any change")
	default:
	}
	mustInsert(t, s.db, "b", "two")

	st := decodeClipPoll(t, awaitClipPoll(t, done))
	if st.Rev != rev+1 || len(st.Changed) != 1 || st.Changed[0].Title != "b" {
		t.Fatalf("got %+v, want the new note at rev %d", st, rev+1)
	}
}

func TestClipStateWaitTimesOut(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{NoAuth: true, clipPollWait: 100 * time.Millisecond})
	mustInsert(t, s.db, "a", "one")

	r := awaitClipPoll(t, startClipPoll(s, context.Background(), "since=1&wait"))
	if r.elapsed < 100*time.Millisecond {
		t.Fatalf("returned after %v, before the wait elapsed", r.elapsed)
	}
	if st := decodeClipPoll(t, r); st.Rev != 1 || len(st.Changed) != 0 || st.Ids != nil {
		t.Fatalf("got %+v, want only rev 1", st)
	}
}

func TestClipStateWaitEndsOnClientCancel(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{NoAuth: true, clipPollWait: time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	done := startClipPoll(s, ctx, "since=0&wait")
	time.Sleep(50 * time.Millisecond)
	cancel()
	awaitClipPoll(t, done)
}

func TestClipStateWaitEndsOnShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := newTestServer(t, Config{Root: t.TempDir(), NoAuth: true, Addr: addr, clipPollWait: time.Minute}, db)
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.ListenAndServe(ctx) }()

	url := "http://" + addr + "/api/clip/state?since=0&wait"
	var resp *http.Response
	respc := make(chan error, 1)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	go func() {
		var err error
		resp, err = http.Get(url)
		respc <- err
	}()
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	stop()
	select {
	case err := <-respc:
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("held poll not released on shutdown")
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("shutdown took %v", d)
	}
}
