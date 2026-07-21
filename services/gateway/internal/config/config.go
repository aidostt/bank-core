package config

import (
	"github.com/aidostt/bank-core/pkg/config"
)

type Config struct {
	HTTPAddr        string
	RedisAddr       string
	JWKSURL         string
	JWTIssuer       string
	IdentityAddr    string
	AccountAddr     string
	TransferAddr    string
	RateLimitReads  int
	RateLimitWrites int
	OTLPEndpoint    string
}

func Load() (Config, error) {
	l := config.New()
	cfg := Config{
		HTTPAddr:        l.StringDefault("GATEWAY_HTTP_ADDR", ":8080"),
		RedisAddr:       l.String("GATEWAY_REDIS_ADDR"),
		JWKSURL:         l.String("GATEWAY_JWKS_URL"),
		JWTIssuer:       l.StringDefault("GATEWAY_JWT_ISSUER", "bank-core-identity"),
		IdentityAddr:    l.String("GATEWAY_IDENTITY_ADDR"),
		AccountAddr:     l.String("GATEWAY_ACCOUNT_ADDR"),
		TransferAddr:    l.String("GATEWAY_TRANSFER_ADDR"),
		RateLimitReads:  l.Int("GATEWAY_RATE_LIMIT_READS_RPS", 10),
		RateLimitWrites: l.Int("GATEWAY_RATE_LIMIT_TRANSFERS_RPS", 2),
		OTLPEndpoint:    l.StringDefault("GATEWAY_OTLP_ENDPOINT", ""),
	}
	return cfg, l.Err()
}
