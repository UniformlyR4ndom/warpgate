package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"log"
	"net"
	"path/filepath"
)

// wsFrameSizeMin and wsFrameSizeMax bound both the -ws-frame-max flag and
// the per-request frameSize a client may specify in a WS control message.
const (
	wsFrameSizeMin = 1024              // 1 KiB
	wsFrameSizeMax = 256 * 1024 * 1024 // 256 MiB
)

type Config struct {
	Root       string
	UlRoot     string
	DBPath     string
	Addr       string
	Cert       string
	CertKey    string
	ClientRoot string
	ClientFP   string
	Key        string
	NoAuth     bool
	WSFrameMax int
}

func main() {
	var cfg Config
	var wsFrameMaxStr string

	flag.StringVar(&cfg.Root, "root", "./files", "root directory to serve files from")
	flag.StringVar(&cfg.UlRoot, "ul-root", "", "directory to accept file uploads into (uploads disabled if not set)")
	flag.StringVar(&cfg.DBPath, "db", "warpgate.sqlite", "path to the SQLite database file")
	flag.StringVar(&cfg.Addr, "listen", "0.0.0.0:8080", "address to listen on (host:port)")
	flag.StringVar(&cfg.Cert, "cert", "", "TLS certificate (chain) file; requires -cert-key")
	flag.StringVar(&cfg.CertKey, "cert-key", "", "TLS private key file; requires -cert")
	flag.StringVar(&cfg.ClientRoot, "client-root", "", "PEM certificate used to validate client certificates; requires -cert and -cert-key")
	flag.StringVar(&cfg.ClientFP, "client-fp", "", "hex SHA-256 fingerprint of the client certificate to accept; requires -cert and -cert-key")
	flag.StringVar(&cfg.Key, "auth-key", "", "authentication key for X-Auth header (auto-generated if not set)")
	flag.BoolVar(&cfg.NoAuth, "no-auth", false, "disable authentication")
	flag.StringVar(&wsFrameMaxStr, "ws-frame-max", "16M",
		"Maximum WebSocket frame size: a byte count, or a number with a K or M suffix (case-insensitive, e.g. 512K, 4M); range 1K-256M. "+
			"Clients may request a smaller per-transfer frame size (via the UI) up to this ceiling; smaller frames are more likely to pass through proxies that buffer whole frames for inspection")
	flag.Parse()

	if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
		log.Fatalf("invalid --listen address %q: %v", cfg.Addr, err)
	}

	wsFrameMax, err := parseByteSize(wsFrameMaxStr)
	if err != nil {
		log.Fatalf("invalid -ws-frame-max %q: %v", wsFrameMaxStr, err)
	}
	if wsFrameMax < wsFrameSizeMin || wsFrameMax > wsFrameSizeMax {
		log.Fatalf("-ws-frame-max must be between %d and %d bytes (got %d)", wsFrameSizeMin, wsFrameSizeMax, wsFrameMax)
	}
	cfg.WSFrameMax = int(wsFrameMax)

	if (cfg.Cert == "") != (cfg.CertKey == "") {
		log.Fatalf("-cert and -cert-key must be provided together")
	}

	if cfg.ClientRoot != "" && cfg.Cert == "" {
		log.Fatalf("-client-root requires -cert and -cert-key")
	}

	if cfg.ClientFP != "" && cfg.Cert == "" {
		log.Fatalf("-client-fp requires -cert and -cert-key")
	}

	if cfg.CertKey != "" {
		certKeyAbs, err := filepath.Abs(cfg.CertKey)
		if err != nil {
			log.Fatalf("resolving -cert-key path: %v", err)
		}
		rootAbs, err := filepath.Abs(cfg.Root)
		if err != nil {
			log.Fatalf("resolving -root path: %v", err)
		}
		if pathIsUnder(certKeyAbs, rootAbs) {
			log.Fatalf("-cert-key %q is inside the download root %q; refusing to start", cfg.CertKey, cfg.Root)
		}
		if cfg.UlRoot != "" {
			ulRootAbs, err := filepath.Abs(cfg.UlRoot)
			if err != nil {
				log.Fatalf("resolving -ul-root path: %v", err)
			}
			if pathIsUnder(certKeyAbs, ulRootAbs) {
				log.Fatalf("-cert-key %q is inside the upload root %q; refusing to start", cfg.CertKey, cfg.UlRoot)
			}
		}
	}

	if cfg.NoAuth && cfg.Key != "" {
		log.Fatalf("-no-auth and -auth-key are mutually exclusive")
	}

	if !cfg.NoAuth && cfg.Key == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			log.Fatalf("generating auth key: %v", err)
		}
		cfg.Key = hex.EncodeToString(b)
		log.Printf("auth key: %s", cfg.Key)
	}

	db, err := OpenDB(cfg.DBPath)
	if err != nil {
		log.Fatalf("opening database: %v", err)
	}
	defer db.Close()

	srv, err := NewServer(cfg, db)
	if err != nil {
		log.Fatalf("initializing server: %v", err)
	}

	proto := "http"
	if cfg.Cert != "" {
		proto = "https"
	}
	log.Printf("listening on %s://%s, serving files from %s", proto, cfg.Addr, srv.absRoot)
	if srv.absUlRoot != "" {
		log.Printf("accepting uploads into %s", srv.absUlRoot)
	}
	if cfg.NoAuth {
		log.Printf("authentication disabled")
	}
	if cfg.ClientRoot != "" {
		log.Printf("requiring TLS client certificate (root: %s)", cfg.ClientRoot)
	}
	if cfg.ClientFP != "" {
		log.Printf("requiring TLS client certificate fingerprint: %s", cfg.ClientFP)
	}
	log.Printf("WebSocket frame size ceiling: %d bytes", cfg.WSFrameMax)
	log.Fatal(srv.ListenAndServe())
}
