// Package erase implements account erasure (openpak/prds#120): everything demonware keeps for an
// OpenPak account is deleted. The service keys its players by the network service account id
// (the account's "switch" link subject, in sessions) and by the nx-baas PID (sessions, CTR
// wallets). PIDs are found from stored sessions and, as a fallback, from nx-baas.
package erase

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	accountv1 "github.com/openpak/account-api/openpak/account/v1"

	"github.com/openpak/demonware/session"
	"github.com/openpak/demonware/titles/ctr"
)

const Service = "demonware"

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// LinkLister answers which switch subjects the core still holds for an account.
type LinkLister interface {
	SwitchSubjects(ctx context.Context, accountID string) ([]string, error)
}

// CoreLinks is the read-only gRPC client of the account core.
type CoreLinks struct{ links accountv1.LinksClient }

func NewCoreLinks(addr, key string) (*CoreLinks, error) {
	if len(key) < 32 {
		return nil, errors.New("ACCOUNT_INTERNAL_KEY must be set (>=32 bytes)")
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any,
			cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			return invoker(metadata.AppendToOutgoingContext(ctx, "x-internal-key", key), method, req, reply, cc, opts...)
		}))
	if err != nil {
		return nil, err
	}
	return &CoreLinks{links: accountv1.NewLinksClient(conn)}, nil
}

func (c *CoreLinks) SwitchSubjects(ctx context.Context, accountID string) ([]string, error) {
	resp, err := c.links.ListAccountLinks(ctx, &accountv1.ListAccountLinksRequest{AccountId: accountID, Namespace: "switch"})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range resp.GetLinks() {
		if l.GetNamespace() == "switch" && l.GetSubjectId() != "" {
			out = append(out, l.GetSubjectId())
		}
	}
	return out, nil
}

// Eraser deletes an account's data.
type Eraser struct {
	Links     LinkLister
	Sessions  *session.Store
	WalletDir string                                                // CTR wallet files when there is no database
	PIDOf     func(ctx context.Context, sub string) (uint64, error) // nx-baas lookup, optional
	Forget    func(pid uint64)                                      // drops cached identity, optional
	Log       *slog.Logger
}

// Erase removes sessions and CTR wallets of the account. Idempotent.
func (e *Eraser) Erase(ctx context.Context, accountID string) (int, error) {
	if e.Links == nil {
		return 0, errors.New("account core not configured")
	}
	subs, err := e.Links.SwitchSubjects(ctx, accountID)
	if err != nil {
		return 0, err
	}
	if len(subs) == 0 {
		e.log(accountID, 0)
		return 0, nil
	}
	pids, err := e.Sessions.PIDsFor(ctx, subs)
	if err != nil {
		return 0, err
	}
	if e.PIDOf != nil {
		seen := map[uint64]bool{}
		for _, p := range pids {
			seen[p] = true
		}
		for _, sub := range subs {
			if p, err := e.PIDOf(ctx, sub); err == nil && p != 0 && !seen[p] {
				seen[p] = true
				pids = append(pids, p)
			}
		}
	}
	// Wallets first: the PIDs were found through the sessions, so a failed retry still has them.
	rows, err := ctr.EraseWallets(ctx, e.Sessions.Pool(), e.WalletDir, pids)
	if err != nil {
		return 0, err
	}
	n, err := e.Sessions.Erase(ctx, subs, pids)
	rows += n
	if err != nil {
		return rows, err
	}
	if e.Forget != nil {
		for _, p := range pids {
			e.Forget(p)
		}
	}
	e.log(accountID, rows)
	return rows, nil
}

func (e *Eraser) log(accountID string, rows int) {
	if e.Log != nil {
		e.Log.Info("account erased", "account", accountID, "rows", rows, "service", Service)
	}
}

// Handler serves DELETE /internal/accounts/{account_id}/data; key "" answers 503.
func (e *Eraser) Handler(key string) http.HandlerFunc {
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if key == "" {
			reply(w, 503, map[string]any{"error": "erase not configured"})
			return
		}
		h := r.Header.Get("Authorization")
		got := strings.TrimPrefix(h, "Bearer ")
		if got == h || subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
			reply(w, 401, map[string]any{"error": "unauthorized"})
			return
		}
		id := r.PathValue("account_id")
		if !uuidRe.MatchString(id) {
			reply(w, 400, map[string]any{"error": "account_id must be a UUID"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		rows, err := e.Erase(ctx, strings.ToLower(id))
		if err != nil {
			if e.Log != nil {
				e.Log.Error("account erase failed", "service", Service, "err", err.Error())
			}
			reply(w, 500, map[string]any{"error": "erase failed"})
			return
		}
		reply(w, 200, map[string]any{"service": Service, "rows": rows})
	}
}
