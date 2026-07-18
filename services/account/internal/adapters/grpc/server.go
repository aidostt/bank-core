// Package grpc maps the account gRPC surface onto app use cases (ADR-0018:
// errors translated exactly once here).
package grpc

import (
	"context"
	"time"

	accountv1 "github.com/aidostt/bank-core/gen/go/bank/account/v1"
	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aidostt/bank-core/services/account/internal/app"
)

type Server struct {
	accountv1.UnimplementedAccountServiceServer
	svc *app.Service
}

func NewServer(svc *app.Service) *Server { return &Server{svc: svc} }

func (s *Server) OpenAccount(ctx context.Context, req *accountv1.OpenAccountRequest) (*accountv1.OpenAccountResponse, error) {
	acc, err := s.svc.OpenAccount(ctx, grpcx.ClaimsFromContext(ctx), req.GetCurrency())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &accountv1.OpenAccountResponse{Account: toProto(acc)}, nil
}

func (s *Server) GetAccount(ctx context.Context, req *accountv1.GetAccountRequest) (*accountv1.GetAccountResponse, error) {
	acc, err := s.svc.GetAccount(ctx, grpcx.ClaimsFromContext(ctx), req.GetAccountId())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &accountv1.GetAccountResponse{Account: toProto(acc)}, nil
}

func (s *Server) ListAccountsByCustomer(ctx context.Context, req *accountv1.ListAccountsByCustomerRequest) (*accountv1.ListAccountsByCustomerResponse, error) {
	items, err := s.svc.ListAccounts(ctx, grpcx.ClaimsFromContext(ctx), req.GetUserId())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	out := make([]*accountv1.AccountWithBalance, 0, len(items))
	for _, it := range items {
		awb := &accountv1.AccountWithBalance{Account: toProto(&it.Account)}
		if it.Balance != nil {
			awb.Balance = toProtoBalance(it.Balance)
		}
		out = append(out, awb)
	}
	return &accountv1.ListAccountsByCustomerResponse{Accounts: out}, nil
}

func (s *Server) ResolveByNumber(ctx context.Context, req *accountv1.ResolveByNumberRequest) (*accountv1.ResolveByNumberResponse, error) {
	acc, err := s.svc.ResolveByNumber(ctx, req.GetNumber())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &accountv1.ResolveByNumberResponse{Account: toProto(acc)}, nil
}

func (s *Server) Freeze(ctx context.Context, req *accountv1.FreezeRequest) (*accountv1.FreezeResponse, error) {
	acc, err := s.svc.Freeze(ctx, grpcx.ClaimsFromContext(ctx), req.GetAccountId(), req.GetReason())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &accountv1.FreezeResponse{Account: toProto(acc)}, nil
}

func (s *Server) Unfreeze(ctx context.Context, req *accountv1.UnfreezeRequest) (*accountv1.UnfreezeResponse, error) {
	acc, err := s.svc.Unfreeze(ctx, grpcx.ClaimsFromContext(ctx), req.GetAccountId())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &accountv1.UnfreezeResponse{Account: toProto(acc)}, nil
}

func (s *Server) GetBalances(ctx context.Context, req *accountv1.GetBalancesRequest) (*accountv1.GetBalancesResponse, error) {
	balances, err := s.svc.GetBalances(ctx, grpcx.ClaimsFromContext(ctx), req.GetAccountIds())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	out := make([]*accountv1.Balance, 0, len(balances))
	for i := range balances {
		out = append(out, toProtoBalance(&balances[i]))
	}
	return &accountv1.GetBalancesResponse{Balances: out}, nil
}

func (s *Server) ListTransactions(ctx context.Context, req *accountv1.ListTransactionsRequest) (*accountv1.ListTransactionsResponse, error) {
	from := time.Time{}
	to := time.Now()
	if req.GetFrom() != nil {
		from = req.GetFrom().AsTime()
	}
	if req.GetTo() != nil {
		to = req.GetTo().AsTime()
	}
	txs, next, err := s.svc.ListTransactions(ctx, grpcx.ClaimsFromContext(ctx),
		req.GetAccountId(), from, to, req.GetPageSize(), req.GetCursor())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	out := make([]*accountv1.Transaction, 0, len(txs))
	for _, t := range txs {
		out = append(out, &accountv1.Transaction{
			EntryId:    t.EntryID,
			AccountId:  t.AccountID,
			Amount:     t.Amount,
			Currency:   t.Currency,
			OccurredAt: timestamppb.New(t.OccurredAt),
		})
	}
	return &accountv1.ListTransactionsResponse{Transactions: out, NextCursor: next}, nil
}

func toProto(a *app.AccountView) *accountv1.Account {
	return &accountv1.Account{
		Id:         a.ID,
		CustomerId: a.CustomerID,
		UserId:     a.UserID,
		Number:     a.Number,
		Currency:   a.Currency,
		Status:     a.Status,
		OpenedAt:   timestamppb.New(a.OpenedAt),
	}
}

func toProtoBalance(b *app.BalanceView) *accountv1.Balance {
	return &accountv1.Balance{
		AccountId: b.AccountID,
		Balance:   b.Balance,
		Held:      b.Held,
		Available: b.Available,
		AsOf:      timestamppb.New(b.AsOf),
	}
}
