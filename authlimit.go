package main

import (
	"net/http"
	"net/netip"
	"sync"
	"time"
)

const (
	authFailMax     = 10
	authFailWindow  = 10 * time.Minute
	authLimitMaxIPs = 100_000
)

// authLimiter blocks a client once it has authFailMax failed authentications
// within authFailWindow; the block lifts as its failures age out of the
// window. IPv6 clients are keyed by /64, since one host typically controls a
// whole /64. Once authLimitMaxIPs clients are tracked, further ones share a
// single overflow entry.
type authLimiter struct {
	now func() time.Time

	mu    sync.Mutex
	fails map[netip.Addr][]time.Time
}

func newAuthLimiter() *authLimiter {
	return &authLimiter{now: time.Now, fails: map[netip.Addr][]time.Time{}}
}

func authLimitKey(a netip.Addr) netip.Addr {
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.Addr()
	}
	return a
}

// recent drops failures older than the window and returns the rest.
func (l *authLimiter) recent(key netip.Addr) []time.Time {
	cutoff := l.now().Add(-authFailWindow)
	ts := l.fails[key]
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(l.fails, key)
	} else {
		l.fails[key] = ts
	}
	return ts
}

func (l *authLimiter) resolve(a netip.Addr) netip.Addr {
	key := authLimitKey(a)
	if _, ok := l.fails[key]; ok || len(l.fails) < authLimitMaxIPs {
		return key
	}
	for k := range l.fails {
		l.recent(k)
	}
	if len(l.fails) < authLimitMaxIPs {
		return key
	}
	return netip.Addr{}
}

func (l *authLimiter) blocked(a netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(l.resolve(a))) >= authFailMax
}

func (l *authLimiter) fail(a netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := l.resolve(a)
	ts := append(l.recent(key), l.now())
	if len(ts) > authFailMax {
		ts = ts[len(ts)-authFailMax:]
	}
	l.fails[key] = ts
}

// failToken records a rejected token; a missing one is no guess.
func (l *authLimiter) failToken(a netip.Addr, token string) {
	if token != "" {
		l.fail(a)
	}
}

// authClient is the address failed authentications are counted under: the
// client IP, or the TCP peer when the client is unknown.
func (s *Server) authClient(r *http.Request) netip.Addr {
	if a := clientIP(r, s.trustedProxies); a.IsValid() {
		return a
	}
	a, _ := parseHostAddr(r.RemoteAddr)
	return a
}

const authBlockedMsg = "too many failed authentication attempts; retry later"
