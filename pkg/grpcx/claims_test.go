package grpcx

import (
	"context"
	"testing"
)

func TestClaimsHelpers(t *testing.T) {
	c := Claims{CustomerID: "u1", Roles: []string{RoleCustomer, RoleSupport}}
	if !c.HasRole(RoleCustomer) || !c.HasRole(RoleSupport) || c.HasRole(RoleAdmin) {
		t.Fatal("HasRole")
	}
	if !c.IsStaff() {
		t.Fatal("support is staff")
	}
	if (Claims{Roles: []string{RoleCustomer}}).IsStaff() {
		t.Fatal("plain customer is not staff")
	}
	if (Claims{Roles: []string{RoleAdmin}}).IsStaff() != true {
		t.Fatal("admin is staff")
	}
}

func TestContextRoundTrips(t *testing.T) {
	ctx := ContextWithClaims(context.Background(), Claims{CustomerID: "u9", Roles: []string{"admin"}})
	if ClaimsFromContext(ctx).CustomerID != "u9" {
		t.Fatal("claims round trip")
	}
	if ClaimsFromContext(context.Background()).CustomerID != "" {
		t.Fatal("empty claims from bare ctx")
	}
	ctx = ContextWithIdempotencyKey(ctx, "key-1")
	if IdempotencyKeyFromContext(ctx) != "key-1" {
		t.Fatal("idem key round trip")
	}
	if IdempotencyKeyFromContext(context.Background()) != "" {
		t.Fatal("empty idem key from bare ctx")
	}
}

func TestParseRoles(t *testing.T) {
	got := parseRoles(" customer , support ,, ")
	if len(got) != 2 || got[0] != "customer" || got[1] != "support" {
		t.Fatalf("parseRoles = %v", got)
	}
	if parseRoles("") != nil {
		t.Fatal("empty → nil")
	}
}

func TestAmbiguousNil(t *testing.T) {
	if Ambiguous(nil) {
		t.Fatal("nil is not ambiguous")
	}
}
