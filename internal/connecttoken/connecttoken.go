// Package connecttoken encodes what a client needs to reach a warpgate server,
// apart from the auth key, as one alphanumeric token: "wg1" + base58 of tagged
// fields and a 4-byte checksum.
package connecttoken

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
)

const (
	prefix    = "wg1"
	sumLen    = 4
	PinLen    = 16
	keyFPLen  = 2
	flagTLS   = 1
	flagMTLS  = 2
	tagFlags  = 1
	tagHost   = 2
	tagPort   = 3
	tagPrefix = 4
	tagReg    = 5
	tagPin    = 6
	tagKeyFP  = 7
)

// Token is the decoded content. Empty fields are absent from the encoding.
type Token struct {
	TLS        bool
	ClientCert bool   // the server requires a TLS client certificate
	Host       string // "" when the server didn't know its public host
	Port       uint16 // 0 = the scheme's default
	Prefix     string // path prefix segment, without "/"
	RegPath    string // IP registration path segment, without "/"
	CertPin    []byte // first PinLen bytes of the SHA-256 of the leaf certificate
	KeyFP      []byte // authhash fingerprint of the key, 2 bytes
}

// Pin returns the certificate pin for a DER-encoded leaf certificate.
func Pin(certDER []byte) []byte {
	sum := sha256.Sum256(certDER)
	return sum[:PinLen]
}

// Encode returns the token string.
func (t Token) Encode() string {
	var b []byte
	field := func(tag byte, v []byte) {
		b = append(b, tag)
		b = binary.AppendUvarint(b, uint64(len(v)))
		b = append(b, v...)
	}
	var flags byte
	if t.TLS {
		flags |= flagTLS
	}
	if t.ClientCert {
		flags |= flagMTLS
	}
	field(tagFlags, []byte{flags})
	if t.Host != "" {
		field(tagHost, []byte(t.Host))
	}
	if t.Port != 0 {
		field(tagPort, binary.BigEndian.AppendUint16(nil, t.Port))
	}
	if t.Prefix != "" {
		field(tagPrefix, []byte(t.Prefix))
	}
	if t.RegPath != "" {
		field(tagReg, []byte(t.RegPath))
	}
	if t.CertPin != nil {
		field(tagPin, t.CertPin)
	}
	if t.KeyFP != nil {
		field(tagKeyFP, t.KeyFP)
	}
	sum := sha256.Sum256(b)
	return prefix + base58Encode(append(b, sum[:sumLen]...))
}

var errDamaged = errors.New("connect token is damaged or incomplete (copy it again)")

// Decode parses a token string. Unknown tags are skipped, so newer servers
// may add fields.
func Decode(s string) (Token, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, prefix) {
		return Token{}, fmt.Errorf("not a connect token (must start with %q)", prefix)
	}
	raw, err := base58Decode(s[len(prefix):])
	if err != nil || len(raw) < sumLen {
		return Token{}, errDamaged
	}
	b, sum := raw[:len(raw)-sumLen], raw[len(raw)-sumLen:]
	want := sha256.Sum256(b)
	if !bytes.Equal(sum, want[:sumLen]) {
		return Token{}, errDamaged
	}
	var t Token
	for len(b) > 0 {
		tag := b[0]
		n, k := binary.Uvarint(b[1:])
		if k <= 0 || n > uint64(len(b)-1-k) {
			return Token{}, errDamaged
		}
		v := b[1+k : 1+k+int(n)]
		b = b[1+k+int(n):]
		switch tag {
		case tagFlags:
			if len(v) != 1 {
				return Token{}, errDamaged
			}
			t.TLS = v[0]&flagTLS != 0
			t.ClientCert = v[0]&flagMTLS != 0
		case tagHost:
			t.Host = string(v)
		case tagPort:
			if len(v) != 2 {
				return Token{}, errDamaged
			}
			t.Port = binary.BigEndian.Uint16(v)
		case tagPrefix:
			t.Prefix = string(v)
		case tagReg:
			t.RegPath = string(v)
		case tagPin:
			if len(v) != PinLen {
				return Token{}, errDamaged
			}
			t.CertPin = v
		case tagKeyFP:
			if len(v) != keyFPLen {
				return Token{}, errDamaged
			}
			t.KeyFP = v
		}
	}
	return t, nil
}

// BaseURL returns the server's base URL, using host (host or host:port) in
// place of the token's host when not empty. Without a port in either, the
// token's port applies.
func (t Token) BaseURL(host string) (string, error) {
	if host == "" {
		host = t.Host
	}
	if host == "" {
		return "", errors.New("the connect token carries no host (the server didn't know its public address); pass -host")
	}
	if _, _, err := net.SplitHostPort(host); err != nil && t.Port != 0 {
		host = net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(int(t.Port)))
	}
	scheme := "http"
	if t.TLS {
		scheme = "https"
	}
	u := scheme + "://" + host
	if t.Prefix != "" {
		u += "/" + t.Prefix
	}
	return u, nil
}

// RegistrationURL returns the IP registration URL for a server with a
// dedicated registration path, else "".
func (t Token) RegistrationURL(base string) string {
	if t.RegPath == "" {
		return ""
	}
	return base + "/" + t.RegPath
}

const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func base58Encode(b []byte) string {
	n := new(big.Int).SetBytes(b)
	base, mod := big.NewInt(58), new(big.Int)
	var out []byte
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		out = append(out, alphabet[mod.Int64()])
	}
	for _, c := range b {
		if c != 0 {
			break
		}
		out = append(out, alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func base58Decode(s string) ([]byte, error) {
	n, base := new(big.Int), big.NewInt(58)
	zeros := 0
	for i, c := range []byte(s) {
		d := strings.IndexByte(alphabet, c)
		if d < 0 {
			return nil, fmt.Errorf("invalid character %q", c)
		}
		if d == 0 && i == zeros {
			zeros++
		}
		n.Mul(n, base).Add(n, big.NewInt(int64(d)))
	}
	return append(make([]byte, zeros), n.Bytes()...), nil
}
