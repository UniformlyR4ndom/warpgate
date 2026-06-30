package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"log"
	"net"
	"path/filepath"
)

type Config struct {
	Root       string
	UlRoot     string
	Addr       string
	Cert       string
	CertKey    string
	ClientRoot string
	ClientFP   string
	Key        string
	NoAuth     bool
}

func main() {
	var cfg Config

	flag.StringVar(&cfg.Root, "root", "./files", "root directory to serve files from")
	flag.StringVar(&cfg.UlRoot, "ul-root", "", "directory to accept file uploads into (uploads disabled if not set)")
	flag.StringVar(&cfg.Addr, "listen", "0.0.0.0:8080", "address to listen on (host:port)")
	flag.StringVar(&cfg.Cert, "cert", "", "TLS certificate (chain) file; requires -cert-key")
	flag.StringVar(&cfg.CertKey, "cert-key", "", "TLS private key file; requires -cert")
	flag.StringVar(&cfg.ClientRoot, "client-root", "", "PEM certificate used to validate client certificates; requires -cert and -cert-key")
	flag.StringVar(&cfg.ClientFP, "client-fp", "", "hex SHA-256 fingerprint of the client certificate to accept; requires -cert and -cert-key")
	flag.StringVar(&cfg.Key, "auth-key", "", "authentication key for X-Auth header (auto-generated if not set)")
	flag.BoolVar(&cfg.NoAuth, "no-auth", false, "disable authentication")
	flag.Parse()

	if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
		log.Fatalf("invalid --listen address %q: %v", cfg.Addr, err)
	}

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

	if !cfg.NoAuth && cfg.Key == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			log.Fatalf("generating auth key: %v", err)
		}
		cfg.Key = hex.EncodeToString(b)
		log.Printf("auth key: %s", cfg.Key)
	}

	srv, err := NewServer(cfg)
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
	log.Fatal(srv.ListenAndServe())
}
