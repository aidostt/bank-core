package grpcclients

import (
	"context"
	"net"
	"testing"

	accountv1 "github.com/aidostt/bank-core/gen/go/bank/account/v1"
	identityv1 "github.com/aidostt/bank-core/gen/go/bank/identity/v1"
	transferv1 "github.com/aidostt/bank-core/gen/go/bank/transfer/v1"
	"github.com/aidostt/bank-core/pkg/logging"
	"google.golang.org/grpc"
)

type fakeIdentity struct {
	identityv1.UnimplementedIdentityServiceServer
}

func (fakeIdentity) GetMe(context.Context, *identityv1.GetMeRequest) (*identityv1.GetMeResponse, error) {
	return &identityv1.GetMeResponse{User: &identityv1.User{Id: "u1"}}, nil
}

func serve(t *testing.T, register func(*grpc.Server)) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	register(s)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

func TestDialAndCall(t *testing.T) {
	idAddr := serve(t, func(s *grpc.Server) { identityv1.RegisterIdentityServiceServer(s, fakeIdentity{}) })
	acAddr := serve(t, func(s *grpc.Server) {
		accountv1.RegisterAccountServiceServer(s, accountv1.UnimplementedAccountServiceServer{})
	})
	trAddr := serve(t, func(s *grpc.Server) {
		transferv1.RegisterTransferServiceServer(s, transferv1.UnimplementedTransferServiceServer{})
	})

	clients, err := Dial(idAddr, acAddr, trAddr, logging.New("test"))
	if err != nil {
		t.Fatal(err)
	}
	defer clients.Close()

	if clients.Identity == nil || clients.Account == nil || clients.Transfer == nil {
		t.Fatal("nil client")
	}
	// A retryable read flows through the standard client chain.
	resp, err := clients.Identity.GetMe(context.Background(), &identityv1.GetMeRequest{})
	if err != nil || resp.GetUser().GetId() != "u1" {
		t.Fatalf("GetMe: %v", err)
	}
}
