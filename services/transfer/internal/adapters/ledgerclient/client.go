// Package ledgerclient adapts the ledger gRPC API to the saga's port.
// Policy (transfer doc): deadline 2s per call; retries only on
// UNAVAILABLE/DEADLINE_EXCEEDED — every ledger method is idempotent by
// design; circuit breaker opens after 5 consecutive failures.
package ledgerclient

import (
	"context"
	"log/slog"

	commonv1 "github.com/aidostt/bank-core/gen/go/bank/common/v1"
	ledgerv1 "github.com/aidostt/bank-core/gen/go/bank/ledger/v1"
	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aidostt/bank-core/services/transfer/internal/app"
)

const referenceType = "transfer"

type Client struct {
	conn *grpc.ClientConn
	api  ledgerv1.LedgerServiceClient
}

func New(addr string, log *slog.Logger) (*Client, error) {
	conn, err := grpcx.Dial(addr, grpcx.ClientConfig{
		Name:            "transfer→ledger",
		RetryableMethod: func(string) bool { return true }, // all idempotent by transfer id
	}, log)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, api: ledgerv1.NewLedgerServiceClient(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) PlaceHold(ctx context.Context, ref app.LedgerAccountRef, amount int64, currency, refID string) (string, error) {
	resp, err := c.api.PlaceHold(ctx, &ledgerv1.PlaceHoldRequest{
		Account:       toProtoRef(ref),
		Amount:        &commonv1.Money{MinorUnits: amount, Currency: currency},
		ReferenceType: referenceType,
		ReferenceId:   refID,
	})
	if err != nil {
		return "", err
	}
	return resp.GetHold().GetId(), nil
}

func (c *Client) ReleaseHold(ctx context.Context, holdID string) error {
	_, err := c.api.ReleaseHold(ctx, &ledgerv1.ReleaseHoldRequest{HoldId: holdID})
	return err
}

func (c *Client) PostTransaction(ctx context.Context, refID, holdID string, postings []app.LedgerPosting) error {
	specs := make([]*ledgerv1.PostingSpec, 0, len(postings))
	for _, p := range postings {
		specs = append(specs, &ledgerv1.PostingSpec{
			Account: toProtoRef(p.Ref),
			Amount:  &commonv1.Money{MinorUnits: p.Amount, Currency: p.Currency},
		})
	}
	_, err := c.api.PostTransaction(ctx, &ledgerv1.PostTransactionRequest{
		ReferenceType: referenceType,
		ReferenceId:   refID,
		HoldId:        holdID,
		Postings:      specs,
	})
	return err
}

func (c *Client) TransactionExists(ctx context.Context, refID string) (bool, error) {
	_, err := c.api.GetTransactionByReference(ctx, &ledgerv1.GetTransactionByReferenceRequest{
		ReferenceType: referenceType,
		ReferenceId:   refID,
	})
	if err == nil {
		return true, nil
	}
	if status.Code(err) == codes.NotFound || apperr.FromGRPC(err).Code == apperr.CodeNotFound {
		return false, nil
	}
	return false, err
}

func toProtoRef(ref app.LedgerAccountRef) *ledgerv1.AccountRef {
	if ref.InternalCode != "" {
		return &ledgerv1.AccountRef{Ref: &ledgerv1.AccountRef_InternalCode{InternalCode: ref.InternalCode}}
	}
	return &ledgerv1.AccountRef{Ref: &ledgerv1.AccountRef_ExternalAccountId{ExternalAccountId: ref.ExternalID}}
}
