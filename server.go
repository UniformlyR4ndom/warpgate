package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gorilla/websocket"
)

type Server struct {
	cfg        Config
	db         *DB
	mux        *http.ServeMux
	absRoot    string
	absUlRoot  string
	clientCAs  *x509.CertPool
	clientFP   []byte
	wsUpgrader websocket.Upgrader
}

func NewServer(cfg Config, db *DB) (*Server, error) {
	absRoot, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("resolving root: %w", err)
	}

	// main.go always validates -ws-frame-max to >= wsFrameSizeMin before
	// this point, but a zero-value Config (e.g. constructed directly by a
	// test) must not silently produce a zero-size frame buffer: that turns
	// the frame-writing loop in ws_download.go into a non-blocking infinite loop
	// (io.ReadFull on an empty buffer returns (0, nil) forever).
	if cfg.WSFrameMax < wsFrameSizeMin {
		cfg.WSFrameMax = wsFrameSizeMin
	}

	s := &Server{
		cfg:     cfg,
		db:      db,
		mux:     http.NewServeMux(),
		absRoot: absRoot,
		wsUpgrader: websocket.Upgrader{
			ReadBufferSize:  cfg.WSFrameMax,
			WriteBufferSize: cfg.WSFrameMax,
		},
	}

	if cfg.ClientRoot != "" {
		pemBytes, err := os.ReadFile(cfg.ClientRoot)
		if err != nil {
			return nil, fmt.Errorf("reading -client-root: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("-client-root %q contains no valid PEM certificates", cfg.ClientRoot)
		}
		s.clientCAs = pool
	}

	if cfg.ClientFP != "" {
		fp, err := hex.DecodeString(cfg.ClientFP)
		if err != nil {
			return nil, fmt.Errorf("decoding -client-fp: %w", err)
		}
		if len(fp) != sha256.Size {
			return nil, fmt.Errorf("-client-fp must be a %d-byte SHA-256 fingerprint (got %d bytes)", sha256.Size, len(fp))
		}
		s.clientFP = fp
	}
	s.mux.HandleFunc("/api/public/checkin", s.handleCheckin)
	s.mux.HandleFunc("/api/public/settings", s.handleSettings)
	s.mux.Handle("/api/clip/list", s.withAuth(s.handleClipList))
	s.mux.Handle("/api/clip/new", s.withAuth(s.handleClipNew))
	s.mux.Handle("/api/clip/edit", s.withAuth(s.handleClipEdit))
	s.mux.Handle("/api/clip/delete", s.withAuth(s.handleClipDelete))
	s.mux.Handle("/api/files/list", s.withAuth(s.handleList))
	s.mux.Handle("/api/files/download", s.withAuth(s.handleDownload))
	// Not wrapped in withAuth: browsers can't set custom headers on a
	// WebSocket handshake, so auth is instead checked inside the handler
	// against the first control message sent after upgrade.
	s.mux.Handle("/api/files/ws/download", http.HandlerFunc(s.handleWSDownload))
	if cfg.UlRoot != "" {
		absUlRoot, err := filepath.Abs(cfg.UlRoot)
		if err != nil {
			return nil, fmt.Errorf("resolving upload root: %w", err)
		}
		s.absUlRoot = absUlRoot
		s.mux.Handle("/api/files/upload", s.withAuth(s.handleUpload))
		s.mux.Handle("/api/files/ul-list", s.withAuth(s.handleUlList))
		s.mux.Handle("/api/files/ws/upload", http.HandlerFunc(s.handleWSUpload))
	}
	return s, nil
}

func (s *Server) withAuth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.NoAuth && r.Header.Get("X-Auth") != s.cfg.Key {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	})
}

func (s *Server) handler() http.Handler {
	static := http.StripPrefix("/static/", staticHandler())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := s.mux.Handler(r)
		if pattern == "" {
			if r.URL.Path == "/" || r.URL.Path == "/static" {
				http.Redirect(w, r, "/static/", http.StatusMovedPermanently)
				return
			}
			static.ServeHTTP(w, r)
			return
		}
		s.mux.ServeHTTP(w, r)
	})
}

func (s *Server) ListenAndServe() error {
	h := s.handler()
	httpSrv := &http.Server{Addr: s.cfg.Addr, Handler: h}
	if s.cfg.Cert != "" {
		if s.clientCAs != nil || s.clientFP != nil {
			tlsCfg := &tls.Config{}
			if s.clientCAs != nil {
				tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
				tlsCfg.ClientCAs = s.clientCAs
			} else {
				tlsCfg.ClientAuth = tls.RequireAnyClientCert
			}
			if s.clientFP != nil {
				fp := s.clientFP
				tlsCfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
					sum := sha256.Sum256(rawCerts[0])
					if !bytes.Equal(sum[:], fp) {
						return fmt.Errorf("client certificate fingerprint mismatch")
					}
					return nil
				}
			}
			httpSrv.TLSConfig = tlsCfg
		}
		return httpSrv.ListenAndServeTLS(s.cfg.Cert, s.cfg.CertKey)
	}
	return httpSrv.ListenAndServe()
}
