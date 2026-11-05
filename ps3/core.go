package ps3

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	accountv1 "github.com/openpak/account-api/openpak/account/v1"
)

// CoreAccounts asks the account core (gRPC) whether an account may play.
type CoreAccounts struct{ ident accountv1.IdentityClient }

// NewCoreAccounts dials the account core with the internal key.
func NewCoreAccounts(addr, key string) (*CoreAccounts, error) {
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
	return &CoreAccounts{ident: accountv1.NewIdentityClient(conn)}, nil
}

// Active is nil for an active account, ErrBanned for a banned, deleted or deletion-pending one.
func (c *CoreAccounts) Active(ctx context.Context, accountID string) error {
	resp, err := c.ident.GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: accountID})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return ErrBanned
		}
		return err
	}
	if resp.GetStatus() != accountv1.AccountStatus_ACCOUNT_STATUS_ACTIVE {
		return ErrBanned
	}
	return nil
}
