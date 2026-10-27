package main

import (
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// regPageHTML registers via a script-submitted POST so that link scanners
// which merely GET the registration URL don't claim a slot.
const regPageHTML = `<!DOCTYPE html><html><head><meta charset="utf-8"><meta name="referrer" content="no-referrer"></head>` +
	`<body><form method="post"></form><script>document.forms[0].submit()</script></body></html>`

// parseIPList parses a comma-separated list of IPs and CIDR ranges.
func parseIPList(s string) ([]netip.Prefix, error) {
	if s == "" {
		return nil, nil
	}
	var out []netip.Prefix
	for _, e := range strings.Split(s, ",") {
		e = strings.TrimSpace(e)
		if strings.Contains(e, "/") {
			p, err := netip.ParsePrefix(e)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q: %w", e, err)
			}
			if p.Addr().Is4In6() {
				p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(e)
		if err != nil {
			return nil, fmt.Errorf("invalid IP %q: %w", e, err)
		}
		a = normalizeAddr(a)
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

func normalizeAddr(a netip.Addr) netip.Addr {
	return a.Unmap().WithZone("")
}

func ipListContains(list []netip.Prefix, a netip.Addr) bool {
	for _, p := range list {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func parseHostAddr(s string) (netip.Addr, error) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return normalizeAddr(ap.Addr()), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, err
	}
	return normalizeAddr(a), nil
}

// clientIP returns the TCP peer address, or — if the peer is a trusted proxy —
// the rightmost untrusted X-Forwarded-For entry. A missing or malformed header
// yields the peer address. The result is invalid only if RemoteAddr is
// unparseable.
func clientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	peer, err := parseHostAddr(r.RemoteAddr)
	if err != nil || !ipListContains(trusted, peer) {
		return peer
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	cur := peer
	for i := len(hops) - 1; i >= 0 && ipListContains(trusted, cur); i-- {
		a, err := parseHostAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return peer
		}
		cur = a
	}
	if ipListContains(trusted, cur) {
		return peer
	}
	return cur
}

// ipFilter admits allowlisted IPs plus IPs that registered via regPath, as
// long as registration is still open.
type ipFilter struct {
	allow    []netip.Prefix
	regPath  string    // path (after prefix stripping) that registers; "" disables registration
	regMax   int       // 0 = unlimited
	regUntil time.Time // zero = no deadline
	now      func() time.Time

	mu         sync.Mutex
	registered map[netip.Addr]struct{}
}

func newIPFilter(cfg Config) (*ipFilter, error) {
	allow, err := parseIPList(cfg.IPAllow)
	if err != nil {
		return nil, fmt.Errorf("-ip-allow: %w", err)
	}
	regPath, err := normalizePathSegment(cfg.IPRegPath)
	if err != nil {
		return nil, fmt.Errorf("-ip-reg-path: %w", err)
	}
	if regPath == "/static" || regPath == "/api" {
		return nil, fmt.Errorf("-ip-reg-path must not be %q", regPath[1:])
	}
	if cfg.IPRegMax < 0 || cfg.IPRegWindow < 0 {
		return nil, fmt.Errorf("-ip-reg-max and -ip-reg-window must not be negative")
	}
	if regPath == "" && (cfg.IPRegMax > 0 || cfg.IPRegWindow > 0) {
		if cfg.PathPrefix == "" {
			return nil, fmt.Errorf("-ip-reg-max/-ip-reg-window without -ip-reg-path register via the base path, which requires -path-prefix")
		}
		regPath = "/"
	}
	if len(allow) == 0 && regPath == "" {
		return nil, nil
	}
	f := &ipFilter{
		allow:      allow,
		regPath:    regPath,
		regMax:     cfg.IPRegMax,
		now:        time.Now,
		registered: map[netip.Addr]struct{}{},
	}
	if cfg.IPRegWindow > 0 {
		f.regUntil = f.now().Add(cfg.IPRegWindow)
	}
	return f, nil
}

func (f *ipFilter) admitted(a netip.Addr) bool {
	if ipListContains(f.allow, a) {
		return true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.registered[a]
	return ok
}

// regOpenLocked reports whether a new IP may still register. f.mu must be held.
func (f *ipFilter) regOpenLocked() bool {
	if !f.regUntil.IsZero() && !f.now().Before(f.regUntil) {
		return false
	}
	return f.regMax == 0 || len(f.registered) < f.regMax
}

func (f *ipFilter) regOpen() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.regOpenLocked()
}

func (f *ipFilter) register(a netip.Addr) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.registered[a]; ok {
		return true
	}
	if !f.regOpenLocked() {
		return false
	}
	f.registered[a] = struct{}{}
	limit := "unlimited"
	if f.regMax > 0 {
		limit = fmt.Sprint(f.regMax)
	}
	log.Printf("ip registration: admitted %s (%d/%s)", a, len(f.registered), limit)
	return true
}

// isRegPath reports whether p (after prefix stripping) serves registration.
// With base-path registration, the UI entry point registers too, so a reload
// after a server restart doesn't need a detour through the base path.
func (f *ipFilter) isRegPath(p string) bool {
	return f.regPath != "" && (p == f.regPath || f.regPath == "/" && p == "/static/")
}

// withIPFilter must run after path-prefix stripping.
func (s *Server) withIPFilter(h http.Handler) http.Handler {
	f := s.ipFilter
	uiURL := s.cfg.PathPrefix + "/static/"
	toUI := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, uiURL, http.StatusSeeOther)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r, s.trustedProxies)
		if !ip.IsValid() {
			writeBlank(w)
			return
		}
		if f.admitted(ip) {
			if f.regPath != "/" && r.URL.Path == f.regPath {
				toUI(w, r)
				return
			}
			h.ServeHTTP(w, r)
			return
		}
		if f.isRegPath(r.URL.Path) {
			switch r.Method {
			case http.MethodPost:
				if f.register(ip) {
					toUI(w, r)
					return
				}
			case http.MethodGet, http.MethodHead:
				if f.regOpen() {
					w.Header().Set("Cache-Control", "no-store")
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					fmt.Fprint(w, regPageHTML)
					return
				}
			}
		}
		writeBlank(w)
	})
}
