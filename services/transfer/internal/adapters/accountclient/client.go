// Package accountclient adapts the account gRPC API to the saga's port.
package accountclient

import (
	"context"
	"log/slog"

	accountv1 "github.com/aidostt/bank-core/gen/go/bank/account/v1"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"google.golang.org/grpc"

	"github.com/aidostt/bank-core/services/transfer/internal/app"
)

type Client struct {
	conn *grpc.ClientConn
	api  accountv1.AccountServiceClient
}

func New(addr string, log *slog.Logger) (*Client, error) {
	conn, err := grpcx.Dial(addr, grpcx.ClientConfig{
		Name:            "transfer→account",
		RetryableMethod: func(string) bool { return true }, // reads only
	}, log)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, api: accountv1.NewAccountServiceClient(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) GetAccount(ctx context.Context, accountID string) (app.AccountInfo, error) {
	resp, err := c.api.GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: accountID})
	if err != nil {
		return app.AccountInfo{}, err
	}
	return fromProto(resp.GetAccount()), nil
}

func (c *Client) ResolveByNumber(ctx context.Context, number string) (app.AccountInfo, error) {
	resp, err := c.api.ResolveByNumber(ctx, &accountv1.ResolveByNumberRequest{Number: number})
	if err != nil {
		return app.AccountInfo{}, err
	}
	return fromProto(resp.GetAccount()), nil
}

func fromProto(a *accountv1.Account) app.AccountInfo {
	return app.AccountInfo{
		ID:       a.GetId(),
		UserID:   a.GetUserId(),
		Number:   a.GetNumber(),
		Currency: a.GetCurrency(),
		Status:   a.GetStatus(),
	}
}
