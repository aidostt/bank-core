// Package grpcclients dials the three backends with the standard client
// policy (CLAUDE.md §3); only naturally idempotent reads are retried.
package grpcclients

import (
	"log/slog"
	"time"

	accountv1 "github.com/aidostt/bank-core/gen/go/bank/account/v1"
	identityv1 "github.com/aidostt/bank-core/gen/go/bank/identity/v1"
	transferv1 "github.com/aidostt/bank-core/gen/go/bank/transfer/v1"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"google.golang.org/grpc"
)

var retryableReads = map[string]bool{
	"/bank.identity.v1.IdentityService/GetMe":                 true,
	"/bank.identity.v1.IdentityService/ListSessions":          true,
	"/bank.account.v1.AccountService/GetAccount":              true,
	"/bank.account.v1.AccountService/ListAccountsByCustomer":  true,
	"/bank.account.v1.AccountService/ResolveByNumber":         true,
	"/bank.account.v1.AccountService/GetBalances":             true,
	"/bank.account.v1.AccountService/ListTransactions":        true,
	"/bank.transfer.v1.TransferService/GetTransfer":           true,
	"/bank.transfer.v1.TransferService/ListTransfers":         true,
	"/bank.transfer.v1.TransferService/GetRates":              true,
}

type Clients struct {
	Identity identityv1.IdentityServiceClient
	Account  accountv1.AccountServiceClient
	Transfer transferv1.TransferServiceClient

	conns []*grpc.ClientConn
}

func Dial(identityAddr, accountAddr, transferAddr string, log *slog.Logger) (*Clients, error) {
	retry := func(method string) bool { return retryableReads[method] }
	c := &Clients{}
	for _, target := range []struct {
		addr    string
		name    string
		timeout time.Duration
		bind    func(conn *grpc.ClientConn)
	}{
		{identityAddr, "gateway→identity", 0, func(conn *grpc.ClientConn) { c.Identity = identityv1.NewIdentityServiceClient(conn) }},
		// OpenAccount fans out to the ledger synchronously; a cold first call
		// can exceed the 2s default. The per-route deadline still caps the
		// total budget (reads 1s / writes 3s / transfers 5s).
		{accountAddr, "gateway→account", 5 * time.Second, func(conn *grpc.ClientConn) { c.Account = accountv1.NewAccountServiceClient(conn) }},
		// CreateTransfer runs the whole saga synchronously in the happy path
		// — give it the transfer-route budget (5s), not the 2s default.
		{transferAddr, "gateway→transfer", 5 * time.Second, func(conn *grpc.ClientConn) { c.Transfer = transferv1.NewTransferServiceClient(conn) }},
	} {
		conn, err := grpcx.Dial(target.addr, grpcx.ClientConfig{
			Name:            target.name,
			Timeout:         target.timeout,
			RetryableMethod: retry,
		}, log)
		if err != nil {
			c.Close()
			return nil, err
		}
		c.conns = append(c.conns, conn)
		target.bind(conn)
	}
	return c, nil
}

func (c *Clients) Close() {
	for _, conn := range c.conns {
		_ = conn.Close()
	}
}
