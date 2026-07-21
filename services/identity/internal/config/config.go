package config

import (
	"strings"
	"time"

	"github.com/aidostt/bank-core/pkg/config"
)

type Config struct {
	GRPCAddr        string
	HTTPAddr        string
	DBDSN           string
	KeysDir         string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	JWTIssuer       string
	KafkaBrokers    []string
	OTLPEndpoint    string
}

func Load() (Config, error) {
	l := config.New()
	cfg := Config{
		GRPCAddr:        l.StringDefault("IDENTITY_GRPC_ADDR", ":9091"),
		HTTPAddr:        l.StringDefault("IDENTITY_HTTP_ADDR", ":8081"),
		DBDSN:           l.String("IDENTITY_DB_DSN"),
		KeysDir:         l.String("IDENTITY_KEYS_DIR"),
		AccessTokenTTL:  l.Duration("IDENTITY_ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTokenTTL: l.Duration("IDENTITY_REFRESH_TOKEN_TTL", 30*24*time.Hour),
		JWTIssuer:       l.StringDefault("IDENTITY_JWT_ISSUER", "bank-core-identity"),
		KafkaBrokers:    strings.Split(l.String("IDENTITY_KAFKA_BROKERS"), ","),
		OTLPEndpoint:    l.StringDefault("IDENTITY_OTLP_ENDPOINT", ""),
	}
	return cfg, l.Err()
}
