// Command demonwared runs OpenPak's Demonware server: auth3 (TLS, behind Traefik), the lobby
// (TCP 3074), NAT probes and introductions (UDP 3074), an optional UDP relay and /health.
// DEMONWARE_TITLES picks the games; README.md lists every variable.
package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	stdlog "log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openpak/demonware"
	"github.com/openpak/demonware/account"
	"github.com/openpak/demonware/auth"
	"github.com/openpak/demonware/lobby"
	"github.com/openpak/demonware/nat"
	"github.com/openpak/demonware/play"
	"github.com/openpak/demonware/redact"
	"github.com/openpak/demonware/session"
	"github.com/openpak/demonware/titles"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// stdLogger is the net/http server's error log, whose messages ("TLS handshake error from
// 203.0.113.7:5000: ...") name the client; addresses are scrubbed before they reach the log.
func stdLogger(l *slog.Logger) *stdlog.Logger {
	return stdlog.New(writerFunc(func(p []byte) {
		l.Debug(redact.Scrub(strings.TrimSpace(string(p))))
	}), "", 0)
}

type writerFunc func([]byte)

func (f writerFunc) Write(p []byte) (int, error) { f(p); return len(p), nil }

func main() {
	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if err := run(log); err != nil {
		log.Error("demonwared", "err", redact.Err(err))
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	data := env("DEMONWARE_DATA", "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		return err
	}
	ttl, err := time.ParseDuration(env("DEMONWARE_SESSION_TTL", "24h"))
	if err != nil {
		return fmt.Errorf("DEMONWARE_SESSION_TTL: %w", err)
	}
	start, err := strconv.ParseInt(env("DEMONWARE_CTR_START_BALANCE", "0"), 10, 64)
	if err != nil || start < 0 {
		return errors.New("DEMONWARE_CTR_START_BALANCE: want a non-negative integer")
	}
	publicIP := net.ParseIP(os.Getenv("DEMONWARE_PUBLIC_IP")).To4()
	if publicIP == nil {
		return errors.New("DEMONWARE_PUBLIC_IP: the IPv4 address consoles reach this server on is required")
	}
	requireAccount := env("DEMONWARE_REQUIRE_ACCOUNT", "1") != "0"
	internalKey := os.Getenv("NX_INTERNAL_KEY")
	if requireAccount && internalKey == "" {
		return errors.New("NX_INTERNAL_KEY is required (or DEMONWARE_REQUIRE_ACCOUNT=0 for a local test)")
	}

	// Titles.
	tenv := titles.Env{DataDir: data, D3Settings: env("DEMONWARE_D3_SETTINGS", filepath.Join(data, "diablo3.json")),
		CTRStart: start, Log: log}
	byID := map[uint32]*demonware.Title{}
	served := map[uint32]bool{}
	var names []string
	for _, slug := range strings.Split(env("DEMONWARE_TITLES", "diablo-3,crash-team-racing"), ",") {
		slug = strings.TrimSpace(slug)
		build, ok := titles.All[slug]
		if !ok {
			return fmt.Errorf("DEMONWARE_TITLES: %q is not a title this binary knows", slug)
		}
		t, err := build(tenv)
		if err != nil {
			return fmt.Errorf("%s: %w", slug, err)
		}
		byID[t.ID], served[t.ID] = t, true
		names = append(names, slug)
	}
	sort.Strings(names)

	sessions, err := session.Open(filepath.Join(data, "sessions.json"), ttl)
	if err != nil {
		return fmt.Errorf("sessions: %w", err)
	}
	accounts := account.New(env("NX_INTERNAL_URL", "http://127.0.0.1:20070"), internalKey)

	// Lobby and the shared services.
	lob := &lobby.Server{Sessions: sessions, Log: log.With("part", "lobby")}
	var signer *auth.Signer
	if path := os.Getenv("DEMONWARE_CTR_SIGNING_KEY"); path != "" {
		if signer, err = auth.LoadSigner(path); err != nil {
			return fmt.Errorf("DEMONWARE_CTR_SIGNING_KEY: %w", err)
		}
		// The CTR key patch swaps the game's embedded key, which the lobby also uses when it
		// wraps the lobby key, so the handshake must try OpenPak's key too.
		lob.PatchedKeys = [][]byte{signer.PublicKeyDER()}
		log.Info("ctr auth replies are signed")
	}
	relay := relayFromEnv(publicIP, log)
	shared := play.New(lob, relay, log.With("part", "play"))
	lob.Handle = func(c *lobby.Conn, r *lobby.Request) lobby.Response {
		if t := byID[c.Title()]; t != nil && t.Handle != nil {
			if resp, ok := t.Handle(c, r); ok {
				return resp
			}
		}
		if resp, ok := shared.Handle(c, r); ok {
			return resp
		}
		c.Log().Debug("task answered with an empty success", "service", r.Service, "task", r.Task)
		return lobby.Response{}
	}
	lob.OnClose(shared.Closed)
	lob.OnClose(func(c *lobby.Conn) {
		if t := byID[c.Title()]; t != nil && t.Closed != nil {
			t.Closed(c)
		}
	})
	for _, addr := range strings.Split(env("LOBBY_LISTEN", ":3074"), ",") {
		ln, err := net.Listen("tcp", strings.TrimSpace(addr))
		if err != nil {
			return fmt.Errorf("lobby: %w", err)
		}
		log.Info("lobby listening", "addr", ln.Addr().String())
		go func() { log.Error("lobby stopped", "err", redact.Err(lob.Serve(ln))) }()
	}

	// NAT probes and introductions.
	natAddr, err := net.ResolveUDPAddr("udp4", env("NAT_LISTEN", ":3074"))
	if err != nil {
		return fmt.Errorf("NAT_LISTEN: %w", err)
	}
	udp, err := net.ListenUDP("udp4", natAddr)
	if err != nil {
		return fmt.Errorf("nat: %w", err)
	}
	natSrv := &nat.Server{Conn: udp, ServerIP: publicIP, Port: natAddr.Port, Log: log.With("part", "nat"),
		Known: func(ip net.IP) bool {
			known := false
			lob.Each(func(c *lobby.Conn) { known = known || c.RemoteIP().Equal(ip) })
			return known
		}}
	log.Info("nat listening", "addr", udp.LocalAddr().String(), "announced", publicIP.String())
	go func() { log.Error("nat stopped", "err", redact.Err(natSrv.Serve())) }()

	// Bans and expiry: a banned account or an expired session leaves the lobby within a minute.
	go func() {
		for range time.Tick(30 * time.Second) {
			lob.Each(func(c *lobby.Conn) {
				if !time.Now().Before(c.Session.Expires) {
					c.Log().Info("session expired")
					c.Close()
					return
				}
				if !requireAccount {
					return
				}
				if err := accounts.Check(c.Player().PID); errors.Is(err, account.ErrBanned) {
					c.Log().Info("account banned: disconnected")
					sessions.Drop(c.Player().PID)
					c.Close()
				}
			})
		}
	}()

	// auth3.
	authSvc := &auth.Service{Accounts: accounts, Sessions: sessions, Titles: served, Open: !requireAccount,
		Log: log.With("part", "auth")}
	authSvc.Signer = signer
	cert, err := tls.LoadX509KeyPair(env("CERT_FILE", "demonware.pem"), env("KEY_FILE", "demonware.key"))
	if err != nil {
		return fmt.Errorf("auth certificate: %w", err)
	}
	authLn, err := net.Listen("tcp", env("AUTH_LISTEN", ":22610"))
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if os.Getenv("DEMONWARE_PROXY_PROTOCOL") == "1" {
		authLn = auth.ProxyListener{Listener: authLn}
	}
	authSrv := &http.Server{
		Handler: authSvc,
		// The games' HTTP client is an old embedded curl.
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS10},
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       30 * time.Second,
		ErrorLog:          stdLogger(log),
	}
	log.Info("auth listening", "addr", authLn.Addr().String(), "proxy_protocol", os.Getenv("DEMONWARE_PROXY_PROTOCOL") == "1",
		"require_account", requireAccount, "titles", names)
	go func() {
		log.Error("auth stopped", "err", redact.Err(authSrv.Serve(tls.NewListener(authLn, authSrv.TLSConfig))))
	}()

	// /health.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "service": "demonware", "titles": names,
			"online": lob.Online(), "games": shared.Games(),
			"logins": authSvc.Logins.Load(), "auth_refused": authSvc.Refusals.Load(),
			"lobby_refused": lob.Refused.Load(), "tasks": lob.Tasks.Load(),
			"nat_probes": natSrv.Probes.Load(), "nat_intros": natSrv.Intros.Load(),
		})
	})
	addr := env("HEALTH_LISTEN", ":22611")
	log.Info("health listening", "addr", addr)
	return http.ListenAndServe(addr, mux)
}

// relayFromEnv builds the optional UDP relay: DEMONWARE_RELAY_PORTS=low-high and
// DEMONWARE_RELAY_FOR="*" or a comma list of online ids (empty: nobody).
func relayFromEnv(ip net.IP, log *slog.Logger) *nat.Relay {
	ports := os.Getenv("DEMONWARE_RELAY_PORTS")
	lo, hi, ok := strings.Cut(ports, "-")
	low, err1 := strconv.Atoi(lo)
	high, err2 := strconv.Atoi(hi)
	if !ok || err1 != nil || err2 != nil || low <= 0 || high < low {
		if ports != "" {
			log.Warn("DEMONWARE_RELAY_PORTS ignored: want low-high", "value", ports)
		}
		return nil
	}
	allow := map[uint64]bool{}
	all := false
	for _, f := range strings.Split(os.Getenv("DEMONWARE_RELAY_FOR"), ",") {
		f = strings.TrimSpace(f)
		if f == "*" {
			all = true
		} else if v, err := strconv.ParseUint(f, 10, 64); err == nil {
			allow[v] = true
		}
	}
	log.Info("relay enabled", "ports", ports, "everyone", all, "players", len(allow))
	return &nat.Relay{IP: ip, Low: low, High: high, Log: log.With("part", "relay"),
		Allow: func(id uint64) bool { return all || allow[id] }}
}
