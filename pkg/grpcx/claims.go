package grpcx

import (
	"context"
	"strings"

	"google.golang.org/grpc/metadata"
)

// Metadata keys carried across every hop (architecture §6): the gateway
// injects them, services trust them (documented threat model — internal
// network is private, mTLS is roadmap).
const (
	MDRequestID      = "x-request-id"
	MDCustomerID     = "x-customer-id"
	MDRoles          = "x-roles"
	MDIdempotencyKey = "x-idempotency-key"
)

const (
	RoleCustomer = "customer"
	RoleSupport  = "support"
	RoleAdmin    = "admin"
)

// Claims are the authenticated caller's identity, extracted from gRPC
// metadata by the server interceptor.
type Claims struct {
	CustomerID string
	Roles      []string
}

func (c Claims) HasRole(role string) bool {
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

func (c Claims) IsStaff() bool {
	return c.HasRole(RoleSupport) || c.HasRole(RoleAdmin)
}

type claimsKey struct{}
type idemKeyKey struct{}

func ContextWithClaims(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}

func ClaimsFromContext(ctx context.Context) Claims {
	if c, ok := ctx.Value(claimsKey{}).(Claims); ok {
		return c
	}
	return Claims{}
}

func ContextWithIdempotencyKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, idemKeyKey{}, key)
}

func IdempotencyKeyFromContext(ctx context.Context) string {
	if k, ok := ctx.Value(idemKeyKey{}).(string); ok {
		return k
	}
	return ""
}

func mdFirst(md metadata.MD, key string) string {
	if vs := md.Get(key); len(vs) > 0 {
		return vs[0]
	}
	return ""
}

func parseRoles(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	roles := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			roles = append(roles, p)
		}
	}
	return roles
}
