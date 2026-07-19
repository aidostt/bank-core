//go:build integration

package app_test

import (
	"context"
	"net"
	"sync"
	"testing"

	accountv1 "github.com/aidostt/bank-core/gen/go/bank/account/v1"
	commonv1 "github.com/aidostt/bank-core/gen/go/bank/common/v1"
	ledgerv1 "github.com/aidostt/bank-core/gen/go/bank/ledger/v1"
	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeLedger implements the LedgerService proto with real idempotency
// semantics (by reference) and scripted failures.
type fakeLedger struct {
	ledgerv1.UnimplementedLedgerServiceServer
	mu sync.Mutex

	holds   map[string]*ledgerv1.Hold // by reference_id
	entries map[string]bool           // by reference_id
	// balances by external account id (available funds), simplistic.
	balances map[string]int64
	frozen   map[string]bool
	released map[string]bool // hold id → released

	failHoldWith    error
	failPostWith    error
	failPostTimes   int // fail N PostTransaction calls, then succeed
	postCalls       int
	releaseCalls    int
	entryOnRecovery bool // pretend the entry exists when queried
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{
		holds:    map[string]*ledgerv1.Hold{},
		entries:  map[string]bool{},
		balances: map[string]int64{},
		frozen:   map[string]bool{},
		released: map[string]bool{},
	}
}

func (f *fakeLedger) PlaceHold(_ context.Context, req *ledgerv1.PlaceHoldRequest) (*ledgerv1.PlaceHoldResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failHoldWith != nil {
		return nil, f.failHoldWith
	}
	if h, ok := f.holds[req.GetReferenceId()]; ok {
		return &ledgerv1.PlaceHoldResponse{Hold: h}, nil
	}
	ext := req.GetAccount().GetExternalAccountId()
	if ext != "" {
		if f.frozen[ext] {
			return nil, apperr.ToGRPC(apperr.New(apperr.CodeAccountFrozen, "account frozen"))
		}
		if f.balances[ext] < req.GetAmount().GetMinorUnits() {
			return nil, apperr.ToGRPC(apperr.New(apperr.CodeInsufficientFunds, "insufficient funds"))
		}
	}
	h := &ledgerv1.Hold{
		Id:          uuid.NewString(),
		ReferenceId: req.GetReferenceId(),
		Amount:      req.GetAmount(),
		Status:      ledgerv1.HoldStatus_HOLD_STATUS_ACTIVE,
	}
	f.holds[req.GetReferenceId()] = h
	return &ledgerv1.PlaceHoldResponse{Hold: h}, nil
}

func (f *fakeLedger) ReleaseHold(_ context.Context, req *ledgerv1.ReleaseHoldRequest) (*ledgerv1.ReleaseHoldResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	f.released[req.GetHoldId()] = true
	return &ledgerv1.ReleaseHoldResponse{Hold: &ledgerv1.Hold{Id: req.GetHoldId(), Status: ledgerv1.HoldStatus_HOLD_STATUS_RELEASED}}, nil
}

func (f *fakeLedger) PostTransaction(_ context.Context, req *ledgerv1.PostTransactionRequest) (*ledgerv1.PostTransactionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.postCalls++
	if f.entries[req.GetReferenceId()] {
		return &ledgerv1.PostTransactionResponse{Entry: &ledgerv1.JournalEntry{ReferenceId: req.GetReferenceId()}}, nil
	}
	if f.failPostTimes > 0 {
		f.failPostTimes--
		return nil, status.Error(codes.Unavailable, "scripted outage")
	}
	if f.failPostWith != nil {
		return nil, f.failPostWith
	}
	// apply simplistic balance movement on external accounts
	for _, p := range req.GetPostings() {
		if ext := p.GetAccount().GetExternalAccountId(); ext != "" {
			f.balances[ext] += p.GetAmount().GetMinorUnits()
		}
	}
	f.entries[req.GetReferenceId()] = true
	return &ledgerv1.PostTransactionResponse{Entry: &ledgerv1.JournalEntry{ReferenceId: req.GetReferenceId()}}, nil
}

func (f *fakeLedger) GetTransactionByReference(_ context.Context, req *ledgerv1.GetTransactionByReferenceRequest) (*ledgerv1.GetTransactionByReferenceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entries[req.GetReferenceId()] || f.entryOnRecovery {
		return &ledgerv1.GetTransactionByReferenceResponse{Entry: &ledgerv1.JournalEntry{ReferenceId: req.GetReferenceId()}}, nil
	}
	return nil, status.Error(codes.NotFound, "no entry")
}

// fakeAccount serves a static account registry.
type fakeAccount struct {
	accountv1.UnimplementedAccountServiceServer
	mu       sync.Mutex
	accounts map[string]*accountv1.Account // by id
	byNumber map[string]*accountv1.Account
}

func newFakeAccount() *fakeAccount {
	return &fakeAccount{accounts: map[string]*accountv1.Account{}, byNumber: map[string]*accountv1.Account{}}
}

func (f *fakeAccount) add(userID, currency, status string) *accountv1.Account {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := &accountv1.Account{
		Id:       uuid.NewString(),
		UserId:   userID,
		Number:   "KZ00125" + uuid.NewString()[:13],
		Currency: currency,
		Status:   status,
	}
	f.accounts[a.Id] = a
	f.byNumber[a.Number] = a
	return a
}

func (f *fakeAccount) GetAccount(_ context.Context, req *accountv1.GetAccountRequest) (*accountv1.GetAccountResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.accounts[req.GetAccountId()]
	if !ok {
		return nil, apperr.ToGRPC(apperr.New(apperr.CodeNotFound, "account not found"))
	}
	return &accountv1.GetAccountResponse{Account: a}, nil
}

func (f *fakeAccount) ResolveByNumber(_ context.Context, req *accountv1.ResolveByNumberRequest) (*accountv1.ResolveByNumberResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.byNumber[req.GetNumber()]
	if !ok {
		return nil, apperr.ToGRPC(apperr.New(apperr.CodeNotFound, "account not found"))
	}
	return &accountv1.ResolveByNumberResponse{Account: a}, nil
}

func serveFakes(t *testing.T, ledger *fakeLedger, account *fakeAccount) (ledgerAddr, accountAddr string) {
	t.Helper()
	ls, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	as, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lsrv := grpc.NewServer()
	ledgerv1.RegisterLedgerServiceServer(lsrv, ledger)
	asrv := grpc.NewServer()
	accountv1.RegisterAccountServiceServer(asrv, account)
	go func() { _ = lsrv.Serve(ls) }()
	go func() { _ = asrv.Serve(as) }()
	t.Cleanup(lsrv.Stop)
	t.Cleanup(asrv.Stop)
	return ls.Addr().String(), as.Addr().String()
}

func kzt(v int64) *commonv1.Money { return &commonv1.Money{MinorUnits: v, Currency: "KZT"} }
