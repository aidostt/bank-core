// Package grpc maps the transfer gRPC surface onto app use cases.
package grpc

import (
	"context"

	transferv1 "github.com/aidostt/bank-core/gen/go/bank/transfer/v1"
	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"github.com/aidostt/bank-core/pkg/money"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aidostt/bank-core/services/transfer/internal/app"
	"github.com/aidostt/bank-core/services/transfer/internal/domain"
)

type Server struct {
	transferv1.UnimplementedTransferServiceServer
	svc *app.Service
}

func NewServer(svc *app.Service) *Server { return &Server{svc: svc} }

func (s *Server) CreateTransfer(ctx context.Context, req *transferv1.CreateTransferRequest) (*transferv1.CreateTransferResponse, error) {
	claims := grpcx.ClaimsFromContext(ctx)
	cmd := app.CreateCmd{
		CustomerID:      claims.CustomerID,
		IdempotencyKey:  grpcx.IdempotencyKeyFromContext(ctx),
		Type:            typeFromProto(req.GetType()),
		FromAccountID:   req.GetFromAccountId(),
		ToAccountID:     req.GetToAccountId(),
		ToAccountNumber: req.GetToAccountNumber(),
		Amount:          req.GetAmount().GetMinorUnits(),
		Currency:        req.GetAmount().GetCurrency(),
	}
	t, err := s.svc.CreateTransfer(ctx, cmd)
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &transferv1.CreateTransferResponse{Transfer: app.ToProto(t)}, nil
}

func (s *Server) GetTransfer(ctx context.Context, req *transferv1.GetTransferRequest) (*transferv1.GetTransferResponse, error) {
	t, err := s.svc.GetTransfer(ctx, grpcx.ClaimsFromContext(ctx), req.GetTransferId())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &transferv1.GetTransferResponse{Transfer: app.ToProto(t)}, nil
}

func (s *Server) ListTransfers(ctx context.Context, req *transferv1.ListTransfersRequest) (*transferv1.ListTransfersResponse, error) {
	rows, next, err := s.svc.ListTransfers(ctx, grpcx.ClaimsFromContext(ctx), req.GetPageSize(), req.GetCursor())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	out := make([]*transferv1.TransferView, 0, len(rows))
	for _, t := range rows {
		out = append(out, app.ToProto(t))
	}
	return &transferv1.ListTransfersResponse{Transfers: out, NextCursor: next}, nil
}

func (s *Server) GetRates(ctx context.Context, _ *transferv1.GetRatesRequest) (*transferv1.GetRatesResponse, error) {
	rates, err := s.svc.GetRates(ctx)
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	out := make([]*transferv1.Rate, 0, len(rates))
	for _, r := range rates {
		out = append(out, &transferv1.Rate{
			Pair:      r.Pair,
			Buy:       money.FormatRate(r.BuyMicros),
			Sell:      money.FormatRate(r.SellMicros),
			ValidFrom: timestamppb.New(r.ValidFrom),
		})
	}
	return &transferv1.GetRatesResponse{Rates: out}, nil
}

func typeFromProto(t transferv1.TransferType) domain.Type {
	switch t {
	case transferv1.TransferType_TRANSFER_TYPE_TOPUP:
		return domain.TypeTopup
	case transferv1.TransferType_TRANSFER_TYPE_INTERNAL:
		return domain.TypeInternal
	case transferv1.TransferType_TRANSFER_TYPE_P2P:
		return domain.TypeP2P
	default:
		return domain.Type("")
	}
}
