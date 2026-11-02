package connecttoken

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	full := Token{
		TLS: true, ClientCert: true, Host: "files.example.com", Port: 8443, Prefix: "s3cr3t",
		RegPath: "join", CertPin: bytes.Repeat([]byte{0xab}, PinLen), KeyFP: []byte{0xa5, 0x06},
	}
	for _, tok := range []Token{full, {}, {Port: 8080}} {
		s := tok.Encode()
		if strings.Trim(s, alphabet) != "" {
			t.Errorf("%q has characters outside the base58 alphabet", s)
		}
		got, err := Decode(s)
		if err != nil {
			t.Fatalf("Decode(%q): %v", s, err)
		}
		if !reflect.DeepEqual(got, tok) {
			t.Errorf("round trip: got %+v, want %+v", got, tok)
		}
	}
	t.Logf("full token (%d chars): %s", len(full.Encode()), full.Encode())
}

func TestDecodeRejectsDamage(t *testing.T) {
	s := Token{TLS: true, Host: "h", Prefix: "p"}.Encode()
	flip := []byte(s)
	if flip[5] == 'a' {
		flip[5] = 'b'
	} else {
		flip[5] = 'a'
	}
	for _, bad := range []string{s[:len(s)-1], string(flip), s + "1", "wg1", "xx" + s[2:], s + "0"} {
		if _, err := Decode(bad); err == nil {
			t.Errorf("Decode(%q) succeeded", bad)
		}
	}
}

func TestBase58LeadingZeros(t *testing.T) {
	for _, b := range [][]byte{{0}, {0, 0, 1}, {0, 255}, {}} {
		got, err := base58Decode(base58Encode(b))
		if err != nil || !bytes.Equal(got, b) {
			t.Errorf("round trip of %v: got %v, %v", b, got, err)
		}
	}
}

func TestBaseURL(t *testing.T) {
	cases := []struct {
		tok        Token
		host, want string
	}{
		{Token{TLS: true, Host: "h", Port: 8443, Prefix: "p"}, "", "https://h:8443/p"},
		{Token{Host: "h"}, "", "http://h"},
		{Token{TLS: true, Host: "h", Port: 8443}, "other", "https://other:8443"},
		{Token{TLS: true, Host: "h", Port: 8443}, "other:9000", "https://other:9000"},
		{Token{Port: 8080}, "::1", "http://[::1]:8080"},
		{Token{Port: 8080}, "[::1]", "http://[::1]:8080"},
	}
	for _, c := range cases {
		got, err := c.tok.BaseURL(c.host)
		if err != nil || got != c.want {
			t.Errorf("%+v.BaseURL(%q) = %q, %v; want %q", c.tok, c.host, got, err, c.want)
		}
	}
	if _, err := (Token{Port: 8080}).BaseURL(""); err == nil {
		t.Error("BaseURL without any host succeeded")
	}
}
