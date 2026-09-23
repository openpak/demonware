// Package auth is auth3, the HTTPS service a Demonware title logs in to before it opens the
// lobby (docs/protocol.md §2). It proves the player's NSA id_token through nx-baas, refuses
// banned and unknown accounts, and answers with a ticket the lobby will accept.
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/openpak/demonware/account"
	"github.com/openpak/demonware/session"
)

// Resolver proves an id_token (account.Client in production).
type Resolver interface {
	Resolve(idToken string) (*account.Player, error)
}

// Service is the auth3 HTTP handler.
type Service struct {
	Accounts Resolver
	Sessions *session.Store
	Titles   map[uint32]bool // titles this deployment serves
	Signer   *Signer         // signs CTR replies; nil leaves them unsigned
	Log      *slog.Logger

	// Open admits a player nx-baas cannot prove, under an id derived from the token, instead
	// of refusing. Local testing only; never in production.
	Open bool

	Logins, Refusals atomic.Int64
}

// ServeHTTP routes /auth/ and CTR's umbrella token endpoint.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1.0/tokens/lsg/":
		s.umbrella(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/auth"):
		s.login(w, r)
	default:
		http.NotFound(w, r)
	}
}

// strInt accepts a JSON number or a numeric string: the client sends its integers as strings.
type strInt uint64

func (v *strInt) UnmarshalJSON(b []byte) error {
	n, err := strconv.ParseUint(strings.Trim(string(b), `"`), 10, 64)
	*v = strInt(n)
	return err
}

type loginRequest struct {
	AuthTask  string `json:"auth_task"`
	IVSeed    string `json:"iv_seed"`
	TitleID   strInt `json:"title_id"`
	ExtraData string `json:"extra_data"`
}

// loginReply's field order is the order the client parses in.
type loginReply struct {
	AuthTask     string `json:"auth_task"`
	Code         string `json:"code"`
	IVSeed       string `json:"iv_seed"`
	ClientTicket string `json:"client_ticket"`
	ServerTicket string `json:"server_ticket"`
	ExtraData    string `json:"extra_data"`
}

func (s *Service) refuse(w http.ResponseWriter, log *slog.Logger, reason string, err error) {
	s.Refusals.Add(1)
	log.Info("auth refused", "reason", reason, "err", err)
	http.Error(w, "forbidden", http.StatusForbidden)
}

func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	log := s.Log.With("remote", r.RemoteAddr, "host", r.Host)
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req loginRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	title := uint32(req.TitleID)
	if !s.Titles[title] {
		s.refuse(w, log, "title not served", fmt.Errorf("title %d", title))
		return
	}
	log = log.With("title", title)
	var extra struct {
		Token    string `json:"token"`
		Username string `json:"username"`
	}
	_ = json.Unmarshal([]byte(req.ExtraData), &extra)

	p, err := s.Accounts.Resolve(extra.Token)
	switch {
	case errors.Is(err, account.ErrBanned):
		s.refuse(w, log, "banned", nil)
		return
	case err != nil && s.Open && extra.Token != "":
		p = openPlayer(extra.Token, extra.Username)
		log.Warn("auth: identity not proven, admitted by DEMONWARE_REQUIRE_ACCOUNT=0", "err", err)
	case err != nil:
		s.refuse(w, log, "identity not proven", err)
		return
	}

	sess, err := s.Sessions.Issue(p, title)
	if err != nil {
		log.Error("auth: issue ticket", "err", err)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	task := 79
	if n, err := strconv.Atoi(req.AuthTask); err == nil {
		task = n + 1
	}
	iv := req.IVSeed
	if iv == "" {
		iv = "0"
	}
	extraOut, _ := json.Marshal(map[string]string{
		"nso_subscription_status": "1",
		"extended_data":           "openpak:" + strconv.FormatUint(p.PID, 10),
	})
	ticket := base64.StdEncoding.EncodeToString(sess.Ticket)
	out, _ := json.Marshal(loginReply{
		AuthTask: strconv.Itoa(task), Code: "700", IVSeed: iv,
		ClientTicket: ticket, ServerTicket: ticket, ExtraData: string(extraOut),
	})
	if title == session.TitleCTR && s.Signer != nil {
		sig, err := s.Signer.Sign(out)
		if err != nil {
			log.Error("auth: sign", "err", err)
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("X-Signature", sig)
	}
	s.Logins.Add(1)
	log.Info("auth ok", "pid", p.PID, "nickname", p.Nickname, "auth_task", task)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// umbrella answers CTR's /v1.0/tokens/lsg/ for a live ticket issued here.
func (s *Service) umbrella(w http.ResponseWriter, r *http.Request) {
	log := s.Log.With("remote", r.RemoteAddr, "path", r.URL.Path)
	var req struct {
		Ticket  string `json:"ticket"`
		TitleID strInt `json:"titleID"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.Ticket)
	sess := s.Sessions.Find(raw)
	if err != nil || sess == nil || uint32(req.TitleID) != sess.Title ||
		binary.LittleEndian.Uint32(raw[5:9]) != sess.Title {
		s.Refusals.Add(1)
		log.Info("umbrella refused", "title", uint64(req.TitleID))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	log.Info("umbrella ok", "pid", sess.Player.PID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"umbrellaID":  sess.Player.OnlineID(),
		"accessToken": base64.RawURLEncoding.EncodeToString(token),
		"expires":     time.Now().Add(time.Hour).Unix(),
		"accounts":    []any{},
	})
}
