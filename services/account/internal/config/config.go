package config

import (
	"strings"

	"github.com/aidostt/bank-core/pkg/config"
)

type Config struct {
	GRPCAddr     string
	HTTPAddr     string
	DBDSN        string
	LedgerAddr   string
	KafkaBrokers []string
	OTLPEndpoint string
}

func Load() (Config, error) {
	l := config.New()
	cfg := Config{
		GRPCAddr:     l.StringDefault("ACCOUNT_GRPC_ADDR", ":9092"),
		HTTPAddr:     l.StringDefault("ACCOUNT_HTTP_ADDR", ":8082"),
		DBDSN:        l.String("ACCOUNT_DB_DSN"),
		LedgerAddr:   l.String("ACCOUNT_LEDGER_ADDR"),
		KafkaBrokers: strings.Split(l.String("ACCOUNT_KAFKA_BROKERS"), ","),
		OTLPEndpoint: l.StringDefault("ACCOUNT_OTLP_ENDPOINT", ""),
	}
	return cfg, l.Err()
}
