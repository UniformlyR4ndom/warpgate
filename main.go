package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"warpgate/internal/bytesize"
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
	UlFlat     bool
	DBPath     string
	Addr       string
	Cert       string
	CertKey    string
	ClientRoot string
	ClientFP   string
	Key        string
	NoAuth     bool
	WSFrameMax int
	PathPrefix string // "" or "/<segment>", see normalizePathSegment
	PublicURL  string

	IPAllow        string
	IPRegPath      string
	IPRegMax       int
	IPRegWindow    time.Duration
	TrustedProxies string
}

func registerFlags(fs *flag.FlagSet, cfg *Config, wsFrameMaxStr, completionShell *string) {
	fs.StringVar(&cfg.Root, "root", "./files", "root directory to serve files from")
	fs.StringVar(&cfg.UlRoot, "ul-root", "", "directory to accept file uploads into (uploads disabled if not set)")
	fs.BoolVar(&cfg.UlFlat, "ul-flat", false,
		"keep the upload directory flat: no browsing, downloading or creating subdirectories through the upload endpoints")
	fs.StringVar(&cfg.DBPath, "db", "warpgate.sqlite", "path to the SQLite database file")
	fs.StringVar(&cfg.Addr, "listen", "0.0.0.0:8080", "address to listen on (host:port)")
	fs.StringVar(&cfg.Cert, "cert", "", "TLS certificate (chain) file; requires -cert-key")
	fs.StringVar(&cfg.CertKey, "cert-key", "", "TLS private key file; requires -cert")
	fs.StringVar(&cfg.ClientRoot, "client-root", "", "PEM certificate used to validate client certificates; requires -cert and -cert-key")
	fs.StringVar(&cfg.ClientFP, "client-fp", "", "hex SHA-256 fingerprint of the client certificate to accept; requires -cert and -cert-key")
	fs.StringVar(&cfg.Key, "auth-key", "", "authentication key for X-Auth header (auto-generated if not set)")
	fs.BoolVar(&cfg.NoAuth, "no-auth", false, "disable authentication")
	fs.StringVar(wsFrameMaxStr, "ws-frame-max", "16M",
		"Maximum WebSocket frame size: a byte count, or a number with a K or M suffix (case-insensitive, e.g. 512K, 4M); range 1K-256M. "+
			"Clients may request a smaller per-transfer frame size (via the UI) up to this ceiling; smaller frames are more likely to pass through proxies that buffer whole frames for inspection")
	fs.StringVar(&cfg.PathPrefix, "path-prefix", "",
		"serve UI and API only under this single path segment (e.g. s3cr3t); all other paths get an empty page")
	fs.StringVar(&cfg.IPAllow, "ip-allow", "", "comma-separated IPs/CIDRs that are always served; enables the IP firewall")
	fs.StringVar(&cfg.IPRegPath, "ip-reg-path", "",
		"path segment (under -path-prefix) whose visit registers the client IP for access; enables the IP firewall")
	fs.IntVar(&cfg.IPRegMax, "ip-reg-max", 0,
		"maximum number of registered IPs (0 = unlimited); without -ip-reg-path, registration happens via the -path-prefix base path")
	fs.DurationVar(&cfg.IPRegWindow, "ip-reg-window", 0,
		"accept IP registrations only this long after startup, e.g. 15m (0 = unlimited); without -ip-reg-path, registration happens via the -path-prefix base path")
	fs.StringVar(&cfg.PublicURL, "public-url", "",
		"http(s)://host[:port] under which clients reach this server, for the connect token (default: the certificate's name, else the listen address)")
	fs.StringVar(&cfg.TrustedProxies, "trusted-proxies", "",
		"comma-separated IPs/CIDRs of reverse proxies whose X-Forwarded-For header determines the client IP")
	fs.StringVar(completionShell, "completion", "",
		"print a shell completion script (bash or zsh) and exit")
}

func main() {
	var cfg Config
	var wsFrameMaxStr, completionShell string
	registerFlags(flag.CommandLine, &cfg, &wsFrameMaxStr, &completionShell)
	flag.Parse()

	if completionShell != "" {
		script, err := completionScript(flag.CommandLine, completionShell)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(script)
		return
	}

	pathPrefix, err := normalizePathSegment(cfg.PathPrefix)
	if err != nil {
		log.Fatalf("invalid -path-prefix %q: %v", cfg.PathPrefix, err)
	}
	cfg.PathPrefix = pathPrefix

	if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
		log.Fatalf("invalid --listen address %q: %v", cfg.Addr, err)
	}
	if cfg.PublicURL != "" {
		if _, err := parsePublicURL(cfg.PublicURL); err != nil {
			log.Fatal(err)
		}
	}

	wsFrameMax, err := bytesize.Parse(wsFrameMaxStr)
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

	if cfg.UlFlat && cfg.UlRoot == "" {
		log.Fatalf("-ul-flat requires -ul-root")
	}

	if err := checkDBPath(cfg.DBPath); err != nil {
		log.Fatalf("invalid -db %q: %v", cfg.DBPath, err)
	}
	checkNotServed(cfg)

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
	log.Printf("listening on %s://%s%s/, serving files from %s", proto, cfg.Addr, cfg.PathPrefix, srv.absRoot)
	if srv.absUlRoot != "" {
		log.Printf("accepting uploads into %s", srv.absUlRoot)
	}
	if cfg.NoAuth {
		log.Printf("authentication disabled")
	} else {
		log.Printf("auth key fingerprint: %s", srv.keyFP)
	}
	if cfg.ClientRoot != "" {
		log.Printf("requiring TLS client certificate (root: %s)", cfg.ClientRoot)
	}
	if cfg.ClientFP != "" {
		log.Printf("requiring TLS client certificate fingerprint: %s", cfg.ClientFP)
	}
	if f := srv.ipFilter; f != nil {
		log.Printf("IP firewall enabled: %d allowlisted range(s)", len(f.allow))
		if f.regPath != "" {
			msg := fmt.Sprintf("IP registration via %s%s (max: %d, 0 = unlimited)", cfg.PathPrefix, f.regPath, f.regMax)
			if !f.regUntil.IsZero() {
				msg += ", open until " + f.regUntil.Format(time.RFC3339)
			}
			log.Print(msg)
		}
	}
	log.Printf("WebSocket frame size ceiling: %d bytes", cfg.WSFrameMax)
	logConnectToken(cfg, srv)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = srv.ListenAndServe(ctx)
	srv.Close()
	if err != nil {
		db.Close()
		log.Fatal(err)
	}
	log.Print("shut down")
}

// checkDBPath rejects DSN syntax the SQLite driver would interpret differently
// from the plain path checkNotServed examines.
func checkDBPath(p string) error {
	if strings.HasPrefix(p, "file:") || strings.Contains(p, "?") {
		return errors.New("must be a plain file path (no file: URI, no '?')")
	}
	return nil
}

// checkNotServed refuses to start when the database, certificate or key is
// reachable through a served directory.
func checkNotServed(cfg Config) {
	files := []struct{ flag, path string }{{"-cert", cfg.Cert}, {"-cert-key", cfg.CertKey}}
	if cfg.DBPath != ":memory:" {
		files = append(files, struct{ flag, path string }{"-db", cfg.DBPath})
	}
	dirs := []struct{ flag, path string }{{"-root", cfg.Root}, {"-ul-root", cfg.UlRoot}}
	for _, f := range files {
		for _, d := range dirs {
			if f.path == "" || d.path == "" {
				continue
			}
			inside, err := fileInsideDir(f.path, d.path)
			if err != nil {
				log.Fatalf("checking %s against %s: %v", f.flag, d.flag, err)
			}
			if inside {
				log.Fatalf("%s %q is inside %s %q; refusing to start", f.flag, f.path, d.flag, d.path)
			}
		}
	}
}
