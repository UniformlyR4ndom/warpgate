package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/gorilla/websocket"

	"warpgate/internal/authhash"
	"warpgate/internal/paramcrypt"
)

type Server struct {
	cfg        Config
	db         *DB
	mux        *http.ServeMux
	absRoot    string
	absUlRoot  string
	root       *os.Root
	ulRoot     *os.Root    // nil when uploads are disabled
	ulTmpInfo  os.FileInfo // nil when uploads are disabled
	clientCAs  *x509.CertPool
	clientFP   []byte
	wsUpgrader websocket.Upgrader
	authToken  string
	paramKeys  paramcrypt.Keys // zero value when cfg.NoAuth: parameter encryption is unavailable
	keyFP      string
	kdfSalt    []byte
	authLimit  *authLimiter

	ipFilter       *ipFilter // nil when the IP firewall is disabled
	trustedProxies []netip.Prefix

	uploadSessions    *sessionStore[*ulSession]
	simUploadSessions *sessionStore[*simUlSession]
}

const (
	httpReadHeaderTimeout = 30 * time.Second
	httpIdleTimeout       = 2 * time.Minute
)

var deriveKey = authhash.DeriveKey

func NewServer(cfg Config, db *DB) (*Server, error) {
	absRoot, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("resolving root: %w", err)
	}

	root, err := os.OpenRoot(absRoot)
	if err != nil {
		return nil, fmt.Errorf("opening root: %w", err)
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
		root:    root,
		wsUpgrader: websocket.Upgrader{
			ReadBufferSize:  wsBufferSize,
			WriteBufferSize: wsBufferSize,
		},
		authLimit: newAuthLimiter(),
	}
	if s.kdfSalt, err = db.KDFSalt(); err != nil {
		return nil, err
	}
	if !cfg.NoAuth {
		k, err := deriveKey(cfg.Key, s.kdfSalt)
		if err != nil {
			return nil, err
		}
		s.authToken = authhash.Token(k)
		s.paramKeys = paramcrypt.DeriveKeys(k)
		s.keyFP = authhash.Fingerprint(k)
	}

	if s.ipFilter, err = newIPFilter(cfg); err != nil {
		return nil, err
	}
	if s.trustedProxies, err = parseIPList(cfg.TrustedProxies); err != nil {
		return nil, fmt.Errorf("-trusted-proxies: %w", err)
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
	s.mux.Handle("/api/clip/state", s.withAuth(s.handleClipState))
	s.mux.Handle("/api/clip/new", s.withAuth(s.handleClipNew))
	s.mux.Handle("/api/clip/edit", s.withAuth(s.handleClipEdit))
	s.mux.Handle("/api/clip/delete", s.withAuth(s.handleClipDelete))
	s.mux.Handle("/api/clip/attachment", s.withAuth(s.handleClipAttachmentNew))
	s.mux.Handle("/api/clip/attachment/download", s.withAuth(s.handleClipAttachmentDownload))
	s.mux.Handle("/api/clip/attachment/edit", s.withAuth(s.handleClipAttachmentEdit))
	s.mux.Handle("/api/clip/attachment/delete", s.withAuth(s.handleClipAttachmentDelete))
	// The WS routes are not wrapped in withAuth: browsers can't set custom
	// headers on a WebSocket handshake, so auth is instead checked inside the
	// handler against the first control message sent after upgrade.
	s.registerBrowse("/api/files/", s.root)
	s.mux.Handle("/api/sim/download", s.withAuth(s.handleSimDownload))
	s.mux.Handle("/api/sim/ws/download", http.HandlerFunc(s.handleSimWSDownload))
	if cfg.UlRoot != "" {
		absUlRoot, err := filepath.Abs(cfg.UlRoot)
		if err != nil {
			return nil, fmt.Errorf("resolving upload root: %w", err)
		}
		s.absUlRoot = absUlRoot
		if s.ulRoot, err = os.OpenRoot(absUlRoot); err != nil {
			return nil, fmt.Errorf("opening upload root: %w", err)
		}
		if err := s.initUlTmpDir(); err != nil {
			return nil, err
		}
		s.uploadSessions = newSessionStore[*ulSession]()
		s.simUploadSessions = newSessionStore[*simUlSession]()
		if !cfg.UlFlat {
			s.registerBrowse("/api/ul/", s.ulRoot)
			s.mux.Handle("/api/ul/mkdir", s.withAuth(s.handleMkdir))
		}
		s.mux.Handle("/api/files/upload", s.withAuth(s.handleUpload))
		s.mux.Handle("/api/files/ul-list", s.withAuth(s.handleUlList))
		s.mux.Handle("/api/files/ws/upload", http.HandlerFunc(s.handleWSUpload))
		s.mux.Handle("/api/sim/upload", s.withAuth(s.handleSimUpload))
		s.mux.Handle("/api/sim/ws/upload", http.HandlerFunc(s.handleSimWSUpload))
		go s.ulSweepExpired()
	}
	return s, nil
}

// registerBrowse registers the listing and download routes for one root.
func (s *Server) registerBrowse(prefix string, root *os.Root) {
	s.mux.Handle(prefix+"list", s.withAuth(func(w http.ResponseWriter, r *http.Request) { s.handleList(w, r, root) }))
	s.mux.Handle(prefix+"download", s.withAuth(func(w http.ResponseWriter, r *http.Request) { s.handleDownload(w, r, root) }))
	s.mux.Handle(prefix+"ws/download", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handleWSDownload(w, r, root) }))
}

// Close releases the server's directory handles.
func (s *Server) Close() {
	s.root.Close()
	if s.ulRoot != nil {
		s.ulRoot.Close()
	}
}

// ulSweepExpired runs for the life of the server, purging idle
// chunked-upload sessions from both the real-upload and sim-upload stores.
func (s *Server) ulSweepExpired() {
	ticker := time.NewTicker(ulSweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.uploadSessions.sweepExpired(s.abortUploadSession)
		s.simUploadSessions.sweepExpired(nil) // nothing to clean up beyond the map entry
	}
}

func (s *Server) withAuth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.NoAuth {
			client := s.authClient(r)
			if s.authLimit.blocked(client) {
				http.Error(w, authBlockedMsg, http.StatusTooManyRequests)
				return
			}
			if token := r.Header.Get("X-Auth"); !tokenEqual(token, s.authToken) {
				s.authLimit.failToken(client, token)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		h(w, r)
	})
}

func tokenEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *Server) handler() http.Handler {
	static := http.StripPrefix("/static/", http.FileServer(http.FS(webRoot)))
	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "":
			// Exactly the path prefix: index.html's relative URLs need the trailing slash.
			http.Redirect(w, r, s.cfg.PathPrefix+"/", http.StatusMovedPermanently)
			return
		case "/":
			http.ServeFileFS(w, r, webRoot, "index.html")
			return
		case "/static", "/static/":
			http.Redirect(w, r, s.cfg.PathPrefix+"/", http.StatusMovedPermanently)
			return
		}
		if _, pattern := s.mux.Handler(r); pattern == "" {
			static.ServeHTTP(w, r)
			return
		}
		s.mux.ServeHTTP(w, r)
	})
	if s.ipFilter != nil {
		h = s.withIPFilter(h)
	}
	if s.cfg.PathPrefix != "" {
		h = withPathPrefix(s.cfg.PathPrefix, h)
	}
	return withCSP(h)
}

func (s *Server) ListenAndServe() error {
	h := s.handler()
	httpSrv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           h,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		IdleTimeout:       httpIdleTimeout,
	}
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
