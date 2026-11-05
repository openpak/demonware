// Package ps3 is the PlayStation 3 side of the Demonware server: the Sony-NP-ticket auth and the
// 3DES lobby on TCP 3074 that Call of Duty: Modern Warfare 3 (and other 2010-2011 Demonware
// titles) speak. The Switch lobby lives in package lobby; both share the port and are told apart
// by the first eight bytes (Split). docs/ps3.md has the protocol notes and their sources.
package ps3

import (
	"bufio"
	"context"
	"crypto/cipher"
	"crypto/des"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"time"

	"github.com/openpak/demonware/redact"
	"github.com/openpak/demonware/wire"
)

// Accounts checks an account's standing in the account core.
type Accounts interface {
	// Active returns nil for an active account, ErrBanned for a banned or deleted one.
	Active(ctx context.Context, accountID string) error
}

// ErrBanned is an account that may not play.
var ErrBanned = errors.New("account banned")

// Rewards receives the game sessions the lobby can prove.
type Rewards interface {
	Hosted(title uint32, game []byte, onlineID string) // a host opened a game (the np link subject)
	Ended(game []byte, accountID string)               // the host closed it
	Left(accountID string)                             // the player's lobby connection closed
}

// Config is what the PS3 server needs.
type Config struct {
	Store     *Store
	PublicKey ed25519.PublicKey // verifies np-account's tickets
	Secret    []byte            // keys the lobby session keys (>= 32 bytes)
	PublicIP  net.IP            // announced to the bandwidth test
	Accounts  Accounts          // nil admits any signed ticket (local tests)
	Rewards   Rewards           // nil: no session reports
	TTL       time.Duration     // lobby ticket lifetime
	Log       *slog.Logger
	Files     map[string][]byte // MW3 publisher files by name; nil uses the built-in set
}

// Server serves PS3 Demonware connections.
type Server struct {
	cfg   Config
	files map[string][]byte

	Logins, Refused, Tasks atomic.Int64
	online                 atomic.Int64
}

// New validates cfg and builds the server.
func New(cfg Config) (*Server, error) {
	if cfg.Store == nil || len(cfg.PublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("ps3: a store and the ticket public key are required")
	}
	if len(cfg.Secret) < 32 {
		return nil, errors.New("ps3: the session secret must be at least 32 bytes")
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 24 * time.Hour
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	s := &Server{cfg: cfg, files: cfg.Files}
	if s.files == nil {
		s.files = defaultFiles()
	}
	return s, nil
}

// Online is the number of lobby connections.
func (s *Server) Online() int { return int(s.online.Load()) }

const (
	serverTicketMagic = 0x43445845 // 'EXDC': our 128-byte lobby ticket
	maxFrame          = 1 << 20

	msgAuthRequest = 18
	msgAuthReply   = 19
	msgTaskReply   = 1
	msgConnID      = 4
	msgRawReply    = 5
	msgLogin       = 7

	svcStats      = 4
	svcStorage    = 10
	svcUtility    = 12
	svcBandwidth  = 18
	svcMatchmaker = 21

	bdNoFile = 1000
)

// sessionKey is the 3DES key of a lobby ticket. Nothing but the ticket's hash is stored, so the
// key is derived: HMAC-SHA256(secret, ticket).
func (s *Server) sessionKey(ticket []byte) []byte {
	m := hmac.New(sha256.New, s.cfg.Secret)
	m.Write(ticket)
	return m.Sum(nil)[:24]
}

func frame(typ byte, payload []byte) []byte {
	b := binary.LittleEndian.AppendUint32(nil, uint32(2+len(payload)))
	return append(append(b, 0, typ), payload...)
}

func newCBC(key []byte, iv []byte, enc bool) (cipher.BlockMode, error) {
	b, err := des.NewTripleDESCipher(key)
	if err != nil {
		return nil, err
	}
	if enc {
		return cipher.NewCBCEncrypter(b, iv), nil
	}
	return cipher.NewCBCDecrypter(b, iv), nil
}

// seal builds a server-to-client encrypted frame: u32 len | 01 | u32 seed | 3DES-CBC of
// [ef be ad de][type][body] padded with zeros to 8.
func seal(key []byte, typ byte, body []byte) ([]byte, error) {
	plain := binary.LittleEndian.AppendUint32(nil, 0xDEADBEEF)
	plain = append(append(plain, typ), body...)
	for len(plain)%8 != 0 {
		plain = append(plain, 0)
	}
	var sb [4]byte
	if _, err := rand.Read(sb[:]); err != nil {
		return nil, err
	}
	seed := binary.LittleEndian.Uint32(sb[:])
	mode, err := newCBC(key, IV(seed), true)
	if err != nil {
		return nil, err
	}
	ct := make([]byte, len(plain))
	mode.CryptBlocks(ct, plain)
	f := append([]byte{1}, binary.LittleEndian.AppendUint32(nil, seed)...)
	f = append(f, ct...)
	return append(binary.LittleEndian.AppendUint32(nil, uint32(len(f))), f...), nil
}

func open(key, body []byte) ([]byte, error) {
	if len(body) < 4+8 {
		return nil, errors.New("short encrypted frame")
	}
	seed := binary.LittleEndian.Uint32(body)
	ct := body[4:]
	ct = ct[:len(ct)-len(ct)%8]
	mode, err := newCBC(key, IV(seed), false)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ct))
	mode.CryptBlocks(out, ct)
	return out, nil
}

// authTicket is the 128-byte bdAuthTicket the client decrypts with its cookie.
func authTicket(title uint32, issued, expires time.Time, pid uint64, name string, key []byte) []byte {
	t := make([]byte, 0, 152)
	t = binary.LittleEndian.AppendUint32(t, 0xEFBDADDE)
	t = append(t, 0)
	t = binary.LittleEndian.AppendUint32(t, title)
	t = binary.LittleEndian.AppendUint32(t, uint32(issued.Unix()))
	t = binary.LittleEndian.AppendUint32(t, uint32(expires.Unix()))
	t = binary.LittleEndian.AppendUint64(t, 0)
	t = binary.LittleEndian.AppendUint64(t, pid)
	n := make([]byte, 64)
	copy(n[:63], name)
	t = append(t, n...)
	t = append(t, key...)
	for len(t) < 152 {
		t = append(t, 0)
	}
	return t
}

// Handle serves one connection until it closes. The first eight bytes decide: a lobby
// connection starts with b4|c8 00 00 00 ff ff 00 00, an auth connection with a frame.
func (s *Server) Handle(nc net.Conn) {
	defer nc.Close()
	log := s.cfg.Log.With("part", "ps3")
	br := bufio.NewReaderSize(nc, 64<<10)
	_ = nc.SetReadDeadline(time.Now().Add(30 * time.Second))
	head, err := br.Peek(8)
	if err != nil {
		return
	}
	if head[4] == 0xff && head[5] == 0xff {
		if head[0] != 0xc8 {
			log.Info("lobby refused: not a Modern Warfare 3 header", "first", head[0])
			s.Refused.Add(1)
			return
		}
		_, _ = br.Discard(8)
		s.lobby(nc, br, log)
		return
	}
	s.auth(nc, br, log)
}

func readFrame(nc net.Conn, br *bufio.Reader, idle time.Duration) (enc, typ byte, payload []byte, err error) {
	_ = nc.SetReadDeadline(time.Now().Add(idle))
	var lb [4]byte
	if _, err = io.ReadFull(br, lb[:]); err != nil {
		return
	}
	n := binary.LittleEndian.Uint32(lb[:])
	if n < 2 || n > maxFrame {
		err = errors.New("bad frame length")
		return
	}
	body := make([]byte, n)
	if _, err = io.ReadFull(br, body); err != nil {
		return
	}
	return body[0], body[1], body[2:], nil
}

func (s *Server) auth(nc net.Conn, br *bufio.Reader, log *slog.Logger) {
	for {
		enc, typ, payload, err := readFrame(nc, br, 60*time.Second)
		if err != nil {
			return
		}
		if enc != 0 || typ != msgAuthRequest {
			log.Debug("auth: unexpected message", "enc", enc, "type", typ, "bytes", len(payload))
			continue
		}
		reply, err := s.authReply(payload, log)
		if err != nil {
			s.Refused.Add(1)
			log.Info("auth refused", "reason", err.Error(), "bytes", len(payload))
			return
		}
		_ = nc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := nc.Write(reply); err != nil {
			return
		}
	}
}

func (s *Server) authReply(payload []byte, log *slog.Logger) ([]byte, error) {
	title := newBitReader(payload).u32()
	t, err := ParseTicket(payload, s.cfg.PublicKey, time.Now())
	if err != nil {
		return nil, errors.New("ticket does not verify")
	}
	if len(t.Cookie) != 24 {
		// np-account has to put the game's request cookie in the ticket (docs/ps3.md).
		return nil, errors.New("ticket carries no 24-byte cookie")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s.cfg.Accounts != nil {
		if err := s.cfg.Accounts.Active(ctx, t.AccountID); err != nil {
			if errors.Is(err, ErrBanned) {
				return nil, errors.New("account not active")
			}
			return nil, errors.New("account core unavailable")
		}
	}
	server := make([]byte, 128)
	if _, err := rand.Read(server); err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint32(server, serverTicketMagic)
	pid := PID(t.AccountID)
	now := time.Now()
	sess := Session{PID: pid, AccountID: t.AccountID, OnlineID: t.OnlineID, Title: title, Issued: now, Expires: now.Add(s.cfg.TTL)}
	if err := s.cfg.Store.PutSession(ctx, server, sess); err != nil {
		return nil, errors.New("session store unavailable")
	}
	var sb [4]byte
	_, _ = rand.Read(sb[:])
	seed := binary.LittleEndian.Uint32(sb[:])
	mode, err := newCBC(t.Cookie, IV(seed), true)
	if err != nil {
		return nil, err
	}
	plain := authTicket(title, sess.Issued, sess.Expires, pid, t.OnlineID, s.sessionKey(server))
	enc := make([]byte, len(plain))
	mode.CryptBlocks(enc, plain)
	w := newBitWriter()
	w.u32(700)
	w.u32(seed)
	w.bytes(enc)
	w.bytes(server)
	s.Logins.Add(1)
	log.Info("auth ok", "title", title, "player", redact.Tag(pid))
	return frame(msgAuthReply, w.out()), nil
}

func (s *Server) lobby(nc net.Conn, br *bufio.Reader, log *slog.Logger) {
	s.online.Add(1)
	defer s.online.Add(-1)
	var (
		key  []byte
		sess Session
		tag  string
	)
	defer func() {
		if key != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			n, _ := s.cfg.Store.DeleteGames(ctx, sess.PID, nil)
			cancel()
			if s.cfg.Rewards != nil {
				s.cfg.Rewards.Left(sess.OnlineID)
			}
			log.Info("lobby closed", "player", tag, "games_removed", n)
		}
	}()
	send := func(typ byte, body []byte) bool {
		f, err := seal(key, typ, body)
		if err != nil {
			return false
		}
		_ = nc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, err = nc.Write(f)
		return err == nil
	}
	for {
		idle := 30 * time.Second
		if key != nil {
			idle = 30 * time.Minute
		}
		enc, typ, payload, err := readFrame(nc, br, idle)
		if err != nil {
			return
		}
		switch {
		case enc == 0 && typ == msgLogin:
			ticket := findMagic(payload, serverTicketMagic)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			var ok bool
			if ticket != nil {
				sess, ok, err = s.cfg.Store.Session(ctx, ticket)
			}
			cancel()
			if ticket == nil || !ok || err != nil {
				s.Refused.Add(1)
				log.Info("lobby login refused", "ticket_found", ticket != nil)
				return
			}
			key = s.sessionKey(ticket)
			tag = redact.Tag(sess.PID)
			var id [8]byte
			_, _ = rand.Read(id[:])
			w := &wire.Writer{}
			w.U64(binary.LittleEndian.Uint64(id[:]) >> 1)
			if !send(msgConnID, w.B) {
				return
			}
			log.Info("lobby login", "player", tag, "title", sess.Title)
		case enc == 1 && key != nil:
			plain, err := open(key, append([]byte{typ}, payload...))
			if err != nil || len(plain) < 6 {
				log.Info("lobby: bad encrypted frame", "player", tag)
				return
			}
			s.Tasks.Add(1)
			svc, msg := plain[4], plain[5:]
			if svc == svcBandwidth {
				log.Info("lobby task", "player", tag, "service", svc, "bytes", len(plain)-6)
				if !send(msgRawReply, s.bandwidth(plain[6:])) {
					return
				}
				continue
			}
			reply := s.task(&sess, svc, msg, log.With("player", tag))
			if !send(msgTaskReply, reply) {
				return
			}
		default:
			log.Debug("lobby: ignored message", "enc", enc, "type", typ, "bytes", len(payload))
		}
	}
}
