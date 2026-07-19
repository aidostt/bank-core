// Package grpc maps the ledger gRPC surface onto app use cases.
package grpc

import (
	"context"
	"time"

	commonv1 "github.com/aidostt/bank-core/gen/go/bank/common/v1"
	ledgerv1 "github.com/aidostt/bank-core/gen/go/bank/ledger/v1"
	"github.com/aidostt/bank-core/pkg/apperr"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aidostt/bank-core/services/ledger/internal/app"
	"github.com/aidostt/bank-core/services/ledger/internal/domain"
)

type Server struct {
	ledgerv1.UnimplementedLedgerServiceServer
	svc *app.Service
}

func NewServer(svc *app.Service) *Server { return &Server{svc: svc} }

func (s *Server) CreateAccount(ctx context.Context, req *ledgerv1.CreateAccountRequest) (*ledgerv1.CreateAccountResponse, error) {
	acc, err := s.svc.CreateAccount(ctx, req.GetExternalAccountId(), req.GetCurrency())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &ledgerv1.CreateAccountResponse{Account: accountToProto(acc)}, nil
}

func (s *Server) PlaceHold(ctx context.Context, req *ledgerv1.PlaceHoldRequest) (*ledgerv1.PlaceHoldResponse, error) {
	hold, err := s.svc.PlaceHold(ctx,
		refFromProto(req.GetAccount()),
		req.GetAmount().GetMinorUnits(),
		req.GetAmount().GetCurrency(),
		req.GetReferenceType(), req.GetReferenceId(),
		time.Duration(req.GetExpiresInSeconds())*time.Second,
	)
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &ledgerv1.PlaceHoldResponse{Hold: holdToProto(hold)}, nil
}

func (s *Server) ReleaseHold(ctx context.Context, req *ledgerv1.ReleaseHoldRequest) (*ledgerv1.ReleaseHoldResponse, error) {
	hold, err := s.svc.ReleaseHold(ctx, req.GetHoldId())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &ledgerv1.ReleaseHoldResponse{Hold: holdToProto(hold)}, nil
}

func (s *Server) PostTransaction(ctx context.Context, req *ledgerv1.PostTransactionRequest) (*ledgerv1.PostTransactionResponse, error) {
	specs := make([]app.PostingSpec, 0, len(req.GetPostings()))
	for _, p := range req.GetPostings() {
		specs = append(specs, app.PostingSpec{
			Ref:      refFromProto(p.GetAccount()),
			Amount:   p.GetAmount().GetMinorUnits(),
			Currency: p.GetAmount().GetCurrency(),
		})
	}
	entry, err := s.svc.PostTransaction(ctx, req.GetReferenceType(), req.GetReferenceId(), req.GetHoldId(), specs)
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &ledgerv1.PostTransactionResponse{Entry: entryToProto(entry)}, nil
}

func (s *Server) GetTransactionByReference(ctx context.Context, req *ledgerv1.GetTransactionByReferenceRequest) (*ledgerv1.GetTransactionByReferenceResponse, error) {
	entry, err := s.svc.GetTransactionByReference(ctx, req.GetReferenceType(), req.GetReferenceId())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &ledgerv1.GetTransactionByReferenceResponse{Entry: entryToProto(entry)}, nil
}

func (s *Server) GetBalances(ctx context.Context, req *ledgerv1.GetBalancesRequest) (*ledgerv1.GetBalancesResponse, error) {
	refs := make([]app.AccountRef, 0, len(req.GetAccounts()))
	for _, r := range req.GetAccounts() {
		refs = append(refs, refFromProto(r))
	}
	rows, err := s.svc.GetBalances(ctx, refs)
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	out := make([]*ledgerv1.AccountBalance, 0, len(rows))
	for _, b := range rows {
		out = append(out, &ledgerv1.AccountBalance{
			AccountId:         b.AccountID,
			ExternalAccountId: b.ExternalID,
			Currency:          b.Currency,
			Balance:           b.Balance,
			Held:              b.Held,
			Available:         b.Balance - b.Held,
			Version:           b.Version,
			AsOf:              timestamppb.New(b.AsOf),
		})
	}
	return &ledgerv1.GetBalancesResponse{Balances: out}, nil
}

func (s *Server) ListPostings(ctx context.Context, req *ledgerv1.ListPostingsRequest) (*ledgerv1.ListPostingsResponse, error) {
	var from, to time.Time
	if req.GetFrom() != nil {
		from = req.GetFrom().AsTime()
	}
	if req.GetTo() != nil {
		to = req.GetTo().AsTime()
	}
	rows, next, err := s.svc.ListPostings(ctx, refFromProto(req.GetAccount()), from, to, req.GetPageSize(), req.GetCursor())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	out := make([]*ledgerv1.Posting, 0, len(rows))
	for _, p := range rows {
		out = append(out, postingToProto(p))
	}
	return &ledgerv1.ListPostingsResponse{Postings: out, NextCursor: next}, nil
}

func refFromProto(r *ledgerv1.AccountRef) app.AccountRef {
	switch ref := r.GetRef().(type) {
	case *ledgerv1.AccountRef_ExternalAccountId:
		return app.AccountRef{ExternalID: ref.ExternalAccountId}
	case *ledgerv1.AccountRef_InternalCode:
		return app.AccountRef{InternalCode: ref.InternalCode}
	case *ledgerv1.AccountRef_LedgerAccountId:
		return app.AccountRef{LedgerID: ref.LedgerAccountId}
	default:
		return app.AccountRef{}
	}
}

func accountToProto(a domain.Account) *ledgerv1.LedgerAccount {
	t := ledgerv1.LedgerAccountType_LEDGER_ACCOUNT_TYPE_CUSTOMER
	if a.Type == domain.TypeInternal {
		t = ledgerv1.LedgerAccountType_LEDGER_ACCOUNT_TYPE_INTERNAL
	}
	return &ledgerv1.LedgerAccount{
		Id:                a.ID,
		ExternalAccountId: a.ExternalID,
		InternalCode:      a.InternalCode,
		Type:              t,
		Currency:          a.Currency,
		Status:            a.Status,
	}
}

func holdToProto(h domain.Hold) *ledgerv1.Hold {
	status := ledgerv1.HoldStatus_HOLD_STATUS_UNSPECIFIED
	switch h.Status {
	case domain.HoldActive:
		status = ledgerv1.HoldStatus_HOLD_STATUS_ACTIVE
	case domain.HoldCaptured:
		status = ledgerv1.HoldStatus_HOLD_STATUS_CAPTURED
	case domain.HoldReleased:
		status = ledgerv1.HoldStatus_HOLD_STATUS_RELEASED
	}
	return &ledgerv1.Hold{
		Id:            h.ID,
		AccountId:     h.AccountID,
		Amount:        &commonv1.Money{MinorUnits: h.Amount, Currency: h.Currency},
		ReferenceType: h.ReferenceType,
		ReferenceId:   h.ReferenceID,
		Status:        status,
		ExpiresAt:     timestamppb.New(h.ExpiresAt),
	}
}

func postingToProto(p app.PostingView) *ledgerv1.Posting {
	return &ledgerv1.Posting{
		Id:                p.ID,
		EntryId:           p.EntryID,
		AccountId:         p.AccountID,
		ExternalAccountId: p.ExternalAccountID,
		Amount:            &commonv1.Money{MinorUnits: p.Amount, Currency: p.Currency},
		OccurredAt:        timestamppb.New(p.OccurredAt),
	}
}

func entryToProto(v *app.EntryView) *ledgerv1.JournalEntry {
	out := &ledgerv1.JournalEntry{
		Id:            v.ID,
		ReferenceType: v.ReferenceType,
		ReferenceId:   v.ReferenceID,
		OccurredAt:    timestamppb.New(v.OccurredAt),
	}
	for _, p := range v.Postings {
		out.Postings = append(out.Postings, postingToProto(p))
	}
	return out
}
