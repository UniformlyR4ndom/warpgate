package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"testing"
	"time"

	"warpgate/internal/authhash"
)

// testK replaces scrypt in tests, which would cost ~128 MiB per server.
func testK(passphrase string) []byte {
	return authhash.Label([]byte(passphrase), "test-kdf")
}

func TestMain(m *testing.M) {
	deriveKey = func(p string, _ []byte) ([]byte, error) { return testK(p), nil }
	os.Exit(m.Run())
}

func TestAuthLimiter(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := newAuthLimiter()
	l.now = func() time.Time { return now }
	a := netip.MustParseAddr("192.0.2.1")
	other := netip.MustParseAddr("192.0.2.2")

	for i := 0; i < authFailMax; i++ {
		if l.blocked(a) {
			t.Fatalf("blocked after %d failures", i)
		}
		l.fail(a)
		now = now.Add(time.Minute / 2)
	}
	if !l.blocked(a) {
		t.Fatal("not blocked after authFailMax failures")
	}
	if l.blocked(other) {
		t.Fatal("other IP blocked")
	}
	// The first failure ages out of the window: one more attempt is allowed.
	now = now.Add(authFailWindow - authFailMax*time.Minute/2 + time.Second)
	if l.blocked(a) {
		t.Fatal("still blocked after the oldest failure expired")
	}
}

func TestAuthLimiterIPv6Prefix(t *testing.T) {
	l := newAuthLimiter()
	for i := 0; i < authFailMax; i++ {
		l.fail(netip.MustParseAddr(fmt.Sprintf("2001:db8:1:2::%x", i+1)))
	}
	if !l.blocked(netip.MustParseAddr("2001:db8:1:2:ffff::1")) {
		t.Error("same /64 not blocked")
	}
	if l.blocked(netip.MustParseAddr("2001:db8:1:3::1")) {
		t.Error("different /64 blocked")
	}
}

func TestAuthLimiterOverflow(t *testing.T) {
	l := newAuthLimiter()
	for i := 0; i < authLimitMaxIPs; i++ {
		l.fails[netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})] = []time.Time{l.now()}
	}
	for i := 0; i < authFailMax; i++ {
		l.fail(netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}))
	}
	if !l.blocked(netip.MustParseAddr("203.0.113.9")) {
		t.Error("overflow clients don't share one entry")
	}
	if l.blocked(netip.AddrFrom4([4]byte{10, 0, 0, 1})) {
		t.Error("tracked client affected by overflow")
	}
}

func TestWithAuthBlocks(t *testing.T) {
	srv := newParamDecodeTestServer(t, Config{Key: "right"})
	h := srv.withAuth(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	do := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Auth", token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	good := authhash.Token(testK("right"))
	if c := do(good); c != http.StatusNoContent {
		t.Fatalf("valid token: %d", c)
	}
	for i := 0; i < 2*authFailMax; i++ {
		if c := do(""); c != http.StatusUnauthorized {
			t.Fatalf("missing token: %d", c)
		}
	}
	for i := 0; i < authFailMax; i++ {
		if c := do("wrong"); c != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, c)
		}
	}
	if c := do(good); c != http.StatusTooManyRequests {
		t.Fatalf("blocked client with valid token: %d, want 429", c)
	}
}
