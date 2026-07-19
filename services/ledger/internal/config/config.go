package config

import (
	"strings"
	"time"

	"github.com/aidostt/bank-core/pkg/config"
)

type Config struct {
	GRPCAddr       string
	HTTPAddr       string
	DBDSN          string
	KafkaBrokers   []string
	HoldDefaultTTL time.Duration
}

func Load() (Config, error) {
	l := config.New()
	cfg := Config{
		GRPCAddr:       l.StringDefault("LEDGER_GRPC_ADDR", ":9093"),
		HTTPAddr:       l.StringDefault("LEDGER_HTTP_ADDR", ":8083"),
		DBDSN:          l.String("LEDGER_DB_DSN"),
		KafkaBrokers:   strings.Split(l.String("LEDGER_KAFKA_BROKERS"), ","),
		HoldDefaultTTL: l.Duration("LEDGER_HOLD_DEFAULT_TTL", 10*time.Minute),
	}
	return cfg, l.Err()
}
