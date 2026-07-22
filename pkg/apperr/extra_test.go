package apperr

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConstructorsAndWrap(t *testing.T) {
	base := errors.New("root cause")
	e := Wrap(CodeNotFound, "not here", base)
	if e.Code != CodeNotFound || e.Error() != "not here: root cause" {
		t.Fatalf("Wrap.Error = %q", e.Error())
	}
	if !errors.Is(e, base) {
		t.Fatal("Unwrap should expose the cause")
	}
	f := Newf(CodeInvalidArgument, "bad %d", 7)
	if f.Code != CodeInvalidArgument || f.Error() != "bad 7" {
		t.Fatalf("Newf.Error = %q", f.Error())
	}
	if New(CodeInternal, "x").Error() != "x" {
		t.Fatal("New.Error")
	}
}

func TestCodeOf(t *testing.T) {
	if CodeOf(nil) != CodeInternal {
		t.Fatal("nil → internal")
	}
	if CodeOf(errors.New("plain")) != CodeInternal {
		t.Fatal("plain → internal")
	}
	if CodeOf(New(CodeRateLimited, "x")) != CodeRateLimited {
		t.Fatal("typed → its code")
	}
	// wrapped typed error is still discoverable via errors.As
	if CodeOf(Wrap(CodeAccountFrozen, "w", New(CodeAccountFrozen, "inner"))) != CodeAccountFrozen {
		t.Fatal("wrapped typed")
	}
}

func TestToGRPCEdgeCases(t *testing.T) {
	if ToGRPC(nil) != nil {
		t.Fatal("nil stays nil")
	}
	// context deadline → DeadlineExceeded
	if status.Code(ToGRPC(context.DeadlineExceeded)) != codes.DeadlineExceeded {
		t.Fatal("deadline mapping")
	}
}

func TestFromGRPCTransportCodes(t *testing.T) {
	if FromGRPC(nil) != nil {
		t.Fatal("nil stays nil")
	}
	cases := map[codes.Code]Code{
		codes.Unavailable:       CodeUpstreamUnavailable,
		codes.DeadlineExceeded:  CodeUpstreamUnavailable,
		codes.NotFound:          CodeNotFound,
		codes.AlreadyExists:     CodeAlreadyExists,
		codes.InvalidArgument:   CodeInvalidArgument,
		codes.Unauthenticated:   CodeUnauthenticated,
		codes.PermissionDenied:  CodeForbidden,
		codes.ResourceExhausted: CodeRateLimited,
		codes.Unknown:           CodeInternal,
	}
	for grpcCode, want := range cases {
		got := FromGRPC(status.Error(grpcCode, "x"))
		if got.Code != want {
			t.Errorf("FromGRPC(%s) = %s, want %s", grpcCode, got.Code, want)
		}
	}
	// a non-status error
	if FromGRPC(errors.New("not a status")).Code != CodeInternal {
		t.Fatal("non-status → internal")
	}
}

func TestHTTPTitleFallback(t *testing.T) {
	if HTTPTitle(Code("MADE_UP")) != "Internal error" {
		t.Fatal("unknown code title fallback")
	}
	if HTTPStatus(Code("MADE_UP")) != 500 {
		t.Fatal("unknown code status fallback")
	}
}
