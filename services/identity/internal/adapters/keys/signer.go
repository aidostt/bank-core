// Package keys manages the RS256 signing keypair (ADR-0011): loaded from a
// mounted volume, generated on first boot; kid is the public-key
// fingerprint. Rotation runbook: docs/services/identity-service.md.
package keys

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Signer struct {
	key    *rsa.PrivateKey
	kid    string
	issuer string
	jwks   []byte
}

// Load reads private.pem from dir or generates a new 2048-bit key on first
// boot (persisted so restarts keep the same kid).
func Load(dir, issuer string) (*Signer, error) {
	path := filepath.Join(dir, "private.pem")
	raw, err := os.ReadFile(path) // #nosec G304 -- fixed name inside the configured keys dir
	if os.IsNotExist(err) {
		key, genErr := rsa.GenerateKey(rand.Reader, 2048)
		if genErr != nil {
			return nil, fmt.Errorf("generate rsa key: %w", genErr)
		}
		raw = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
		if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
			return nil, mkErr
		}
		if wErr := os.WriteFile(path, raw, 0o600); wErr != nil {
			return nil, fmt.Errorf("persist key: %w", wErr)
		}
	} else if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in %s", path)
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse key: %w", err)
	}

	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(pubDER)
	kid := hex.EncodeToString(sum[:8])

	jwks, err := json.Marshal(map[string]any{
		"keys": []map[string]string{{
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"kid": kid,
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}},
	})
	if err != nil {
		return nil, err
	}
	return &Signer{key: key, kid: kid, issuer: issuer, jwks: jwks}, nil
}

// SignAccess mints an access token: claims sub/roles/sid (ADR-0011).
func (s *Signer) SignAccess(userID, familyID string, roles []string, ttl time.Duration) (string, error) {
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":   s.issuer,
		"sub":   userID,
		"sid":   familyID,
		"roles": roles,
		"iat":   now.Unix(),
		"exp":   now.Add(ttl).Unix(),
	})
	tok.Header["kid"] = s.kid
	return tok.SignedString(s.key)
}

func (s *Signer) JWKS() []byte { return s.jwks }

func (s *Signer) Public() *rsa.PublicKey { return &s.key.PublicKey }
