package apperr

import (
	"errors"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Every code must be present in every mapping table — the tables are the
// contract (ADR-0018).
var allCodes = []Code{
	CodeInvalidArgument, CodeNotFound, CodeAlreadyExists, CodeUnauthenticated,
	CodeForbidden, CodeInsufficientFunds, CodeAccountFrozen, CodeCurrencyMismatch,
	CodeUnbalancedEntry, CodeLimitExceeded, CodeIdempotencyConflict,
	CodeIdempotencyKeyRequired, CodeRateLimited, CodeUpstreamUnavailable, CodeInternal,
}

func TestMappingTablesComplete(t *testing.T) {
	for _, c := range allCodes {
		if _, ok := grpcCode[c]; !ok {
			t.Errorf("grpcCode missing %s", c)
		}
		if _, ok := httpStatus[c]; !ok {
			t.Errorf("httpStatus missing %s", c)
		}
		if _, ok := httpTitle[c]; !ok {
			t.Errorf("httpTitle missing %s", c)
		}
	}
}

func TestRoundTripThroughGRPC(t *testing.T) {
	for _, c := range allCodes {
		in := New(c, "boom")
		out := FromGRPC(ToGRPC(in))
		if out.Code != c {
			t.Errorf("round trip %s → %s", c, out.Code)
		}
	}
}

func TestUnknownErrorsDoNotLeak(t *testing.T) {
	err := ToGRPC(errors.New("secret database dsn"))
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("want Internal, got %s", st.Code())
	}
	if st.Message() != "internal error" {
		t.Fatalf("internal details leaked: %q", st.Message())
	}
}

func TestBusinessCodesMapToFailedPrecondition(t *testing.T) {
	for _, c := range []Code{CodeInsufficientFunds, CodeAccountFrozen, CodeCurrencyMismatch, CodeUnbalancedEntry, CodeLimitExceeded} {
		st, _ := status.FromError(ToGRPC(New(c, "x")))
		if st.Code() != codes.FailedPrecondition {
			t.Errorf("%s → %s, want FailedPrecondition", c, st.Code())
		}
		if HTTPStatus(c) != http.StatusUnprocessableEntity {
			t.Errorf("%s → HTTP %d, want 422", c, HTTPStatus(c))
		}
	}
}

func TestAmbiguous(t *testing.T) {
	if !Ambiguous(status.Error(codes.Unavailable, "conn refused")) {
		t.Error("Unavailable must be ambiguous")
	}
	if !Ambiguous(status.Error(codes.DeadlineExceeded, "timeout")) {
		t.Error("DeadlineExceeded must be ambiguous")
	}
	if Ambiguous(ToGRPC(New(CodeInsufficientFunds, "no money"))) {
		t.Error("business failure must not be ambiguous")
	}
}

func TestRetryableOnlyUpstream(t *testing.T) {
	for _, c := range allCodes {
		want := c == CodeUpstreamUnavailable
		if Retryable(c) != want {
			t.Errorf("Retryable(%s) = %v", c, !want)
		}
	}
}
