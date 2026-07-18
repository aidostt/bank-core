// Package grpc maps the identity gRPC surface onto app use cases; errors
// are translated exactly once here (ADR-0018).
package grpc

import (
	"context"

	identityv1 "github.com/aidostt/bank-core/gen/go/bank/identity/v1"
	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aidostt/bank-core/services/identity/internal/app"
)

type Server struct {
	identityv1.UnimplementedIdentityServiceServer
	svc *app.Service
}

func NewServer(svc *app.Service) *Server { return &Server{svc: svc} }

func (s *Server) Register(ctx context.Context, req *identityv1.RegisterRequest) (*identityv1.RegisterResponse, error) {
	u, err := s.svc.Register(ctx, req.GetEmail(), req.GetPassword(), req.GetName(), req.GetPhone())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &identityv1.RegisterResponse{User: toProtoUser(u)}, nil
}

func (s *Server) Login(ctx context.Context, req *identityv1.LoginRequest) (*identityv1.LoginResponse, error) {
	pair, err := s.svc.Login(ctx, req.GetEmail(), req.GetPassword(), "", "")
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &identityv1.LoginResponse{
		AccessToken:      pair.AccessToken,
		RefreshToken:     pair.RefreshToken,
		ExpiresInSeconds: pair.ExpiresInSeconds,
		User:             toProtoUser(pair.User),
	}, nil
}

func (s *Server) Refresh(ctx context.Context, req *identityv1.RefreshRequest) (*identityv1.RefreshResponse, error) {
	pair, err := s.svc.Refresh(ctx, req.GetRefreshToken(), "", "")
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &identityv1.RefreshResponse{
		AccessToken:      pair.AccessToken,
		RefreshToken:     pair.RefreshToken,
		ExpiresInSeconds: pair.ExpiresInSeconds,
	}, nil
}

func (s *Server) Logout(ctx context.Context, req *identityv1.LogoutRequest) (*identityv1.LogoutResponse, error) {
	if err := s.svc.Logout(ctx, req.GetRefreshToken()); err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &identityv1.LogoutResponse{}, nil
}

func (s *Server) GetMe(ctx context.Context, _ *identityv1.GetMeRequest) (*identityv1.GetMeResponse, error) {
	claims := grpcx.ClaimsFromContext(ctx)
	u, err := s.svc.GetMe(ctx, claims.CustomerID)
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &identityv1.GetMeResponse{User: toProtoUser(u)}, nil
}

func (s *Server) ListSessions(ctx context.Context, _ *identityv1.ListSessionsRequest) (*identityv1.ListSessionsResponse, error) {
	claims := grpcx.ClaimsFromContext(ctx)
	if claims.CustomerID == "" {
		return nil, apperr.ToGRPC(apperr.New(apperr.CodeUnauthenticated, "no caller identity"))
	}
	sessions, err := s.svc.ListSessions(ctx, claims.CustomerID)
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	out := make([]*identityv1.Session, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, &identityv1.Session{
			Id:        sess.ID,
			FamilyId:  sess.FamilyID,
			CreatedAt: timestamppb.New(sess.CreatedAt),
			ExpiresAt: timestamppb.New(sess.ExpiresAt),
			Ip:        sess.IP,
			UserAgent: sess.UserAgent,
		})
	}
	return &identityv1.ListSessionsResponse{Sessions: out}, nil
}

func (s *Server) RevokeSession(ctx context.Context, req *identityv1.RevokeSessionRequest) (*identityv1.RevokeSessionResponse, error) {
	claims := grpcx.ClaimsFromContext(ctx)
	if claims.CustomerID == "" {
		return nil, apperr.ToGRPC(apperr.New(apperr.CodeUnauthenticated, "no caller identity"))
	}
	if err := s.svc.RevokeSession(ctx, claims.CustomerID, req.GetSessionId()); err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &identityv1.RevokeSessionResponse{}, nil
}

func toProtoUser(u *app.UserView) *identityv1.User {
	if u == nil {
		return nil
	}
	return &identityv1.User{
		Id:        u.ID,
		Email:     u.Email,
		Name:      u.Name,
		Phone:     u.Phone,
		Roles:     u.Roles,
		CreatedAt: timestamppb.New(u.CreatedAt),
	}
}
