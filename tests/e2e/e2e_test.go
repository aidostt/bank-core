//go:build e2e

// Package e2e drives the full platform through the public API against a
// running compose stack (make up && make e2e). The M2 scenario (prompts/
// M2.md §7): money movement with projections converging, a velocity-fraud
// burst that freezes the account and blocks the next transfer, and
// notification feed assertions straight from notification_db.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

var base = func() string {
	if v := os.Getenv("GATEWAY_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}()

var pgHost = func() string {
	if v := os.Getenv("PG_HOST"); v != "" {
		return v
	}
	return "localhost:5432"
}()

type client struct {
	t     *testing.T
	token string
}

func (c *client) do(method, path string, body any, headers map[string]string) (int, []byte) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (c *client) doJSON(method, path string, body any, headers map[string]string, wantStatus int) map[string]any {
	c.t.Helper()
	status, raw := c.do(method, path, body, headers)
	if status != wantStatus {
		c.t.Fatalf("%s %s → %d (want %d): %s", method, path, status, wantStatus, raw)
	}
	out := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			c.t.Fatalf("%s %s: bad json: %v", method, path, err)
		}
	}
	return out
}

func waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("gateway never became ready — run `make up` first")
		}
		time.Sleep(2 * time.Second)
	}
}

func register(t *testing.T, email, password, name string) *client {
	t.Helper()
	c := &client{t: t}
	c.doJSON("POST", "/v1/auth/register",
		map[string]string{"email": email, "password": password, "name": name}, nil, 201)
	login := c.doJSON("POST", "/v1/auth/login",
		map[string]string{"email": email, "password": password}, nil, 200)
	c.token = login["access_token"].(string)
	return c
}

// createTransfer retries on 429 (the transfer route is limited to 2 rps).
func createTransfer(t *testing.T, c *client, body map[string]any, key string) map[string]any {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		status, raw := c.do("POST", "/v1/transfers", body, map[string]string{"Idempotency-Key": key})
		switch status {
		case 201, 202:
			out := map[string]any{}
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			return out
		case 429:
			time.Sleep(700 * time.Millisecond)
		default:
			t.Fatalf("POST /v1/transfers → %d: %s", status, raw)
		}
	}
	t.Fatal("transfer permanently rate limited")
	return nil
}

func queryOne[T any](t *testing.T, dsn, sql string, args ...any) T {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect %s: %v", dsn, err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var out T
	if err := conn.QueryRow(ctx, sql, args...).Scan(&out); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return out
}

func TestEndToEnd(t *testing.T) {
	waitReady(t)
	runID := time.Now().UnixNano()

	alice := register(t, fmt.Sprintf("alice-e2e-%d@demo.kz", runID), "password-123", "Alice E2E")
	bob := register(t, fmt.Sprintf("bob-e2e-%d@demo.kz", runID), "password-123", "Bob E2E")

	aliceAcc := alice.doJSON("POST", "/v1/accounts", map[string]string{"currency": "KZT"}, nil, 201)
	bobAcc := bob.doJSON("POST", "/v1/accounts", map[string]string{"currency": "KZT"}, nil, 201)
	aliceID := aliceAcc["id"].(string)
	bobNumber := bobAcc["number"].(string)

	aliceUserID := alice.doJSON("GET", "/v1/customers/me", nil, nil, 200)["id"].(string)

	// Top up alice: 10,000.00 KZT.
	alice.doJSON("POST", "/v1/topups",
		map[string]any{"account_id": aliceID, "minor_units": 1_000_000, "currency": "KZT"},
		map[string]string{"Idempotency-Key": fmt.Sprintf("e2e-topup-%d", runID)}, 201)

	// The balance shown at the gateway is the M2 projection — poll until the
	// consumer has applied the topup (staleness target <1s, architecture §3).
	waitBalance := func(c *client, accountID string, want int64) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			accounts := c.doJSON("GET", "/v1/accounts", nil, nil, 200)["accounts"].([]any)
			for _, a := range accounts {
				acc := a.(map[string]any)
				if acc["id"] == accountID {
					if b, ok := acc["balance"].(map[string]any); ok {
						if int64(b["balance"].(float64)) == want {
							return
						}
					}
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("projection for %s never reached %d", accountID, want)
			}
			time.Sleep(time.Second)
		}
	}
	waitBalance(alice, aliceID, 1_000_000)

	// Velocity burst (R2: >10 transfers in 5m → HIGH → freeze).
	var lastTransfer map[string]any
	for i := 1; i <= 11; i++ {
		lastTransfer = createTransfer(t, alice, map[string]any{
			"type": "P2P", "from_account_id": aliceID,
			"to_account_number": bobNumber,
			"minor_units":       1000, "currency": "KZT",
		}, fmt.Sprintf("e2e-burst-%d-%d", runID, i))
		if state := lastTransfer["state"].(string); state != "COMPLETED" {
			t.Fatalf("burst transfer %d state = %s", i, state)
		}
		time.Sleep(600 * time.Millisecond)
	}

	// The freeze travels transfers.status → antifraud → fraud.alerts →
	// account consumer. Poll the account status.
	deadline := time.Now().Add(45 * time.Second)
	for {
		accounts := alice.doJSON("GET", "/v1/accounts", nil, nil, 200)["accounts"].([]any)
		status := ""
		for _, a := range accounts {
			acc := a.(map[string]any)
			if acc["id"] == aliceID {
				status = acc["status"].(string)
			}
		}
		if status == "FROZEN" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("account never froze after the velocity burst (status %s)", status)
		}
		time.Sleep(time.Second)
	}

	// The next transfer must be rejected: ACCOUNT_FROZEN.
	blocked := createTransfer(t, alice, map[string]any{
		"type": "P2P", "from_account_id": aliceID,
		"to_account_number": bobNumber,
		"minor_units":       1000, "currency": "KZT",
	}, fmt.Sprintf("e2e-blocked-%d", runID))
	if blocked["state"].(string) != "FAILED" || blocked["reason"].(string) != "ACCOUNT_FROZEN" {
		t.Fatalf("post-freeze transfer: state=%v reason=%v", blocked["state"], blocked["reason"])
	}

	// HIGH alert exists in antifraud_db.
	alerts := queryOne[int](t,
		"postgres://antifraud_user:antifraud_pass@"+pgHost+"/antifraud_db",
		"SELECT count(*) FROM alerts WHERE severity='HIGH' AND customer_id=$1", aliceUserID)
	if alerts < 1 {
		t.Fatalf("HIGH alerts for alice = %d, want ≥1", alerts)
	}

	// Notification rows exist (M2 DoD): completion notifications for the
	// burst and a fraud alert notification.
	notifications := queryOne[int](t,
		"postgres://notification_user:notification_pass@"+pgHost+"/notification_db",
		"SELECT count(*) FROM notifications WHERE user_id=$1 AND template='transfer_completed'", aliceUserID)
	if notifications < 11 {
		t.Fatalf("transfer_completed notifications = %d, want ≥11", notifications)
	}
	fraudNotes := queryOne[int](t,
		"postgres://notification_user:notification_pass@"+pgHost+"/notification_db",
		"SELECT count(*) FROM notifications WHERE user_id=$1 AND template='fraud_alert'", aliceUserID)
	if fraudNotes < 1 {
		t.Fatalf("fraud_alert notifications = %d, want ≥1", fraudNotes)
	}

	// Support path: seeded admin unfreezes; transfers work again.
	admin := &client{t: t}
	adminLogin := admin.doJSON("POST", "/v1/auth/login",
		map[string]string{"email": "admin@bank-core.local", "password": "Adm1n-Demo-Pass"}, nil, 200)
	admin.token = adminLogin["access_token"].(string)
	admin.doJSON("POST", "/v1/admin/accounts/"+aliceID+"/unfreeze", nil, nil, 200)

	again := createTransfer(t, alice, map[string]any{
		"type": "P2P", "from_account_id": aliceID,
		"to_account_number": bobNumber,
		"minor_units":       1000, "currency": "KZT",
	}, fmt.Sprintf("e2e-after-unfreeze-%d", runID))
	if again["state"].(string) != "COMPLETED" {
		t.Fatalf("post-unfreeze transfer state = %v", again["state"])
	}
}
