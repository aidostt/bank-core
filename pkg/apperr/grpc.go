package apperr

import (
	"context"
	"errors"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const errorDomain = "bank-core"

// grpcCode: mapped exactly once at the service edge (ADR-0018).
// Business precondition failures share FAILED_PRECONDITION; the stable code
// travels in errdetails.ErrorInfo.Reason.
var grpcCode = map[Code]codes.Code{
	CodeInvalidArgument:        codes.InvalidArgument,
	CodeNotFound:               codes.NotFound,
	CodeAlreadyExists:          codes.AlreadyExists,
	CodeUnauthenticated:        codes.Unauthenticated,
	CodeForbidden:              codes.PermissionDenied,
	CodeInsufficientFunds:      codes.FailedPrecondition,
	CodeAccountFrozen:          codes.FailedPrecondition,
	CodeCurrencyMismatch:       codes.FailedPrecondition,
	CodeUnbalancedEntry:        codes.FailedPrecondition,
	CodeLimitExceeded:          codes.FailedPrecondition,
	CodeIdempotencyConflict:    codes.FailedPrecondition,
	CodeIdempotencyKeyRequired: codes.InvalidArgument,
	CodeRateLimited:            codes.ResourceExhausted,
	CodeUpstreamUnavailable:    codes.Unavailable,
	CodeInternal:               codes.Internal,
}

// ToGRPC converts an application error into a gRPC status carrying the
// stable code as ErrorInfo. Unknown errors become opaque INTERNAL — details
// never leak to clients (they are logged server-side keyed by request.id).
func ToGRPC(err error) error {
	if err == nil {
		return nil
	}
	var ae *Error
	if !errors.As(err, &ae) {
		if errors.Is(err, context.DeadlineExceeded) {
			return status.Error(codes.DeadlineExceeded, "deadline exceeded")
		}
		return status.Error(codes.Internal, "internal error")
	}
	c, ok := grpcCode[ae.Code]
	if !ok {
		c = codes.Internal
	}
	st := status.New(c, ae.Message)
	withInfo, detErr := st.WithDetails(&errdetails.ErrorInfo{
		Reason: string(ae.Code),
		Domain: errorDomain,
	})
	if detErr != nil {
		return st.Err()
	}
	return withInfo.Err()
}

// FromGRPC reconstructs the typed error from a gRPC error on the client
// side; transport-level failures map to CodeUpstreamUnavailable.
func FromGRPC(err error) *Error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return Wrap(CodeInternal, "non-grpc error", err)
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == errorDomain {
			return &Error{Code: Code(info.GetReason()), Message: st.Message()}
		}
	}
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded:
		return &Error{Code: CodeUpstreamUnavailable, Message: st.Message()}
	case codes.NotFound:
		return &Error{Code: CodeNotFound, Message: st.Message()}
	case codes.AlreadyExists:
		return &Error{Code: CodeAlreadyExists, Message: st.Message()}
	case codes.InvalidArgument:
		return &Error{Code: CodeInvalidArgument, Message: st.Message()}
	case codes.Unauthenticated:
		return &Error{Code: CodeUnauthenticated, Message: st.Message()}
	case codes.PermissionDenied:
		return &Error{Code: CodeForbidden, Message: st.Message()}
	case codes.ResourceExhausted:
		return &Error{Code: CodeRateLimited, Message: st.Message()}
	default:
		return &Error{Code: CodeInternal, Message: st.Message()}
	}
}

// Ambiguous reports whether a gRPC call outcome is unknown (timeout /
// unavailable): the operation may or may not have executed. The transfer
// saga uses this to decide between definitive failure and recovery.
func Ambiguous(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	return st.Code() == codes.Unavailable || st.Code() == codes.DeadlineExceeded
}
