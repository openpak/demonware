// Package account resolves a Switch player through nx-baas, OpenPak's Switch adapter, and
// keeps banned accounts out (website/docs/ban-lookup.md is the contract).
//
// Demonware's auth request carries the player's NSA id_token. nx-baas proves it: an
// emulator's token carries an "openpak" claim, a retail console's does not, so the claim is sent
// when present and the whole id_token otherwise (POST /internal/switch/identity). The reply
// is the player's Switch projection and friends; 403 means banned or being deleted.
package account

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrBanned is nx-baas refusing the account.
var ErrBanned = errors.New("account banned")

// ErrUnknown is a token nx-baas could not tie to an OpenPak account.
var ErrUnknown = errors.New("account not recognised")

// Friend is one entry of a player's friend list.
type Friend struct {
	PID        uint64 `json:"pid"`
	BaasUserID string `json:"baas_user_id"`
	Nickname   string `json:"nickname"`
}

// Player is a proven OpenPak Switch account.
type Player struct {
	PID        uint64   `json:"pid"`
	BaasUserID string   `json:"baas_user_id"` // the network service account id, hex
	Nickname   string   `json:"nickname"`
	Friends    []Friend `json:"friends"`
}

// OnlineID is the id Demonware and the games use for this player: the network service
// account id as a number. It is stable per account and is the id the Switch friend service
// gives the game for this player (docs/protocol.md §9).
func (p *Player) OnlineID() uint64 { return NSAID(p.BaasUserID) }

// NSAID parses a hex network service account id; 0 if it is not one.
func NSAID(hex string) uint64 {
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "0x"), 16, 64)
	if err != nil {
		return 0
	}
	return v
}

// Client talks to nx-baas's internal API.
type Client struct {
	URL, Key string
	HTTP     *http.Client

	mu     sync.Mutex
	active map[uint64]time.Time // pid -> until when an "active" answer is trusted
}

func New(url, key string) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), Key: key,
		HTTP: &http.Client{Timeout: 5 * time.Second}, active: map[uint64]time.Time{}}
}

const activeTTL = 30 * time.Second

func (c *Client) identity(q map[string]any) (*Player, error) {
	body, _ := json.Marshal(q)
	req, err := http.NewRequest(http.MethodPost, c.URL+"/internal/switch/identity", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", c.Key)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nx-baas: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden:
		return nil, ErrBanned
	case http.StatusUnauthorized, http.StatusNotFound:
		return nil, ErrUnknown
	default:
		return nil, fmt.Errorf("nx-baas: %s", resp.Status)
	}
	var p Player
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("nx-baas: %w", err)
	}
	if p.PID == 0 || p.OnlineID() == 0 {
		return nil, fmt.Errorf("nx-baas: incomplete identity")
	}
	c.mu.Lock()
	c.active[p.PID] = time.Now().Add(activeTTL)
	c.mu.Unlock()
	return &p, nil
}

// Resolve proves an NSA id_token and returns its player.
func (c *Client) Resolve(idToken string) (*Player, error) {
	if openpak := openpakClaim(idToken); openpak != "" {
		return c.identity(map[string]any{"openpak": openpak})
	}
	if strings.Count(idToken, ".") != 2 {
		return nil, ErrUnknown
	}
	return c.identity(map[string]any{"id_token": idToken})
}

// Check returns nil when pid may play, ErrBanned when it may not, and another error when
// nx-baas did not answer. Active answers are cached for 30 s; banned ones never are.
func (c *Client) Check(pid uint64) error {
	c.mu.Lock()
	until, ok := c.active[pid]
	c.mu.Unlock()
	if ok && time.Now().Before(until) {
		return nil
	}
	_, err := c.identity(map[string]any{"pid": pid})
	if errors.Is(err, ErrBanned) {
		c.mu.Lock()
		delete(c.active, pid)
		c.mu.Unlock()
	}
	return err
}

// openpakClaim is the id_token's "openpak" claim, or "".
func openpakClaim(tok string) string {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var c struct {
		OpenPak string `json:"openpak"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return ""
	}
	return c.OpenPak
}
