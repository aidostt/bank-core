// Package app: gateway edge logic — JWT validation against the identity
// JWKS (ADR-0011) and the route-level RBAC table.
package app

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrTokenInvalid = errors.New("token invalid")
	ErrUnknownKid   = errors.New("unknown key id")
)

// JWKSCache validates RS256 access tokens with keys fetched from identity;
// unknown kid triggers a throttled refetch (key rotation support).
type JWKSCache struct {
	url    string
	issuer string
	client *http.Client

	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	lastRefresh time.Time
}

func NewJWKSCache(url, issuer string) *JWKSCache {
	return &JWKSCache{
		url:    url,
		issuer: issuer,
		client: &http.Client{Timeout: 5 * time.Second},
		keys:   map[string]*rsa.PublicKey{},
	}
}

// WarmUp fetches the JWKS with retries so the gateway starts serving with
// keys in place (identity may still be booting under compose).
func (c *JWKSCache) WarmUp(deadline time.Duration) error {
	end := time.Now().Add(deadline)
	for {
		err := c.refresh()
		if err == nil {
			return nil
		}
		if time.Now().After(end) {
			return fmt.Errorf("jwks warm-up: %w", err)
		}
		time.Sleep(time.Second)
	}
}

func (c *JWKSCache) refresh() error {
	resp, err := c.client.Get(c.url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var doc struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if len(keys) == 0 {
		return errors.New("jwks contains no usable keys")
	}
	c.mu.Lock()
	c.keys = keys
	c.lastRefresh = time.Now()
	c.mu.Unlock()
	return nil
}

func (c *JWKSCache) key(kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	k, ok := c.keys[kid]
	needsRefresh := !ok && time.Since(c.lastRefresh) > 10*time.Second
	c.mu.Unlock()
	if ok {
		return k, nil
	}
	if needsRefresh {
		if err := c.refresh(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		k, ok = c.keys[kid]
		c.mu.Unlock()
		if ok {
			return k, nil
		}
	}
	return nil, ErrUnknownKid
}

// TokenClaims is the validated caller identity.
type TokenClaims struct {
	UserID    string
	SessionID string
	Roles     []string
}

// Validate parses and verifies an RS256 access token.
func (c *JWKSCache) Validate(raw string) (TokenClaims, error) {
	tok, err := jwt.Parse(raw,
		func(t *jwt.Token) (any, error) {
			kid, _ := t.Header["kid"].(string)
			return c.key(kid)
		},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(c.issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !tok.Valid {
		return TokenClaims{}, ErrTokenInvalid
	}
	mc, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return TokenClaims{}, ErrTokenInvalid
	}
	out := TokenClaims{}
	out.UserID, _ = mc["sub"].(string)
	out.SessionID, _ = mc["sid"].(string)
	if rawRoles, ok := mc["roles"].([]any); ok {
		for _, r := range rawRoles {
			if s, ok := r.(string); ok {
				out.Roles = append(out.Roles, s)
			}
		}
	}
	if out.UserID == "" {
		return TokenClaims{}, ErrTokenInvalid
	}
	return out, nil
}
