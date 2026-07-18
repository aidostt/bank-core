// Package ledgerclient adapts the ledger gRPC API to the app-side port.
// Client policy per CLAUDE.md §3: 2s timeout, 3 retries with jitter (all
// used methods are idempotent), circuit breaker.
package ledgerclient

import (
	"context"
	"log/slog"
	"time"

	ledgerv1 "github.com/aidostt/bank-core/gen/go/bank/ledger/v1"
	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aidostt/bank-core/services/account/internal/app"
)

type Client struct {
	conn *grpc.ClientConn
	api  ledgerv1.LedgerServiceClient
}

func New(addr string, log *slog.Logger) (*Client, error) {
	conn, err := grpcx.Dial(addr, grpcx.ClientConfig{
		Name: "account→ledger",
		// CreateAccount is idempotent by external_account_id; the reads are
		// naturally idempotent — everything may be retried.
		RetryableMethod: func(string) bool { return true },
	}, log)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, api: ledgerv1.NewLedgerServiceClient(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) CreateAccount(ctx context.Context, externalAccountID, currency string) error {
	_, err := c.api.CreateAccount(ctx, &ledgerv1.CreateAccountRequest{
		ExternalAccountId: externalAccountID,
		Currency:          currency,
	})
	if err != nil {
		return apperr.FromGRPC(err)
	}
	return nil
}

func (c *Client) GetBalances(ctx context.Context, externalAccountIDs []string) (map[string]app.BalanceView, error) {
	refs := make([]*ledgerv1.AccountRef, 0, len(externalAccountIDs))
	for _, id := range externalAccountIDs {
		refs = append(refs, &ledgerv1.AccountRef{Ref: &ledgerv1.AccountRef_ExternalAccountId{ExternalAccountId: id}})
	}
	resp, err := c.api.GetBalances(ctx, &ledgerv1.GetBalancesRequest{Accounts: refs})
	if err != nil {
		return nil, apperr.FromGRPC(err)
	}
	out := make(map[string]app.BalanceView, len(resp.GetBalances()))
	for _, b := range resp.GetBalances() {
		out[b.GetExternalAccountId()] = app.BalanceView{
			AccountID: b.GetExternalAccountId(),
			Balance:   b.GetBalance(),
			Held:      b.GetHeld(),
			Available: b.GetAvailable(),
			AsOf:      b.GetAsOf().AsTime(),
		}
	}
	return out, nil
}

func (c *Client) ListPostings(ctx context.Context, externalAccountID string, from, to time.Time, pageSize int32, cursor string) ([]app.TransactionView, string, error) {
	resp, err := c.api.ListPostings(ctx, &ledgerv1.ListPostingsRequest{
		Account:  &ledgerv1.AccountRef{Ref: &ledgerv1.AccountRef_ExternalAccountId{ExternalAccountId: externalAccountID}},
		From:     timestamppb.New(from),
		To:       timestamppb.New(to),
		PageSize: pageSize,
		Cursor:   cursor,
	})
	if err != nil {
		return nil, "", apperr.FromGRPC(err)
	}
	out := make([]app.TransactionView, 0, len(resp.GetPostings()))
	for _, p := range resp.GetPostings() {
		out = append(out, app.TransactionView{
			EntryID:    p.GetEntryId(),
			AccountID:  p.GetExternalAccountId(),
			Amount:     p.GetAmount().GetMinorUnits(),
			Currency:   p.GetAmount().GetCurrency(),
			OccurredAt: p.GetOccurredAt().AsTime(),
		})
	}
	return out, resp.GetNextCursor(), nil
}
