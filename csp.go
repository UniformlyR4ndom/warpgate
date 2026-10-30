package main

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
)

// contentSecurityPolicy is sent on every response. The frontend has no inline
// scripts/styles and never evaluates strings as code, so 'self' suffices.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' blob:; connect-src 'self'; worker-src 'self'; object-src 'none'; " +
	"base-uri 'none'; frame-ancestors 'none'; form-action 'none'"

// regPageCSP replaces contentSecurityPolicy on the registration page, whose
// inline script is allowed by hash.
var regPageCSP = func() string {
	sum := sha256.Sum256([]byte(regPageScript))
	return "default-src 'none'; script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; " +
		"form-action 'self'; base-uri 'none'; frame-ancestors 'none'"
}()

func withCSP(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		h.ServeHTTP(w, r)
	})
}
