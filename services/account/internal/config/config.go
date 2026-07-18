package config

import (
	"github.com/aidostt/bank-core/pkg/config"
)

type Config struct {
	GRPCAddr   string
	HTTPAddr   string
	DBDSN      string
	LedgerAddr string
}

func Load() (Config, error) {
	l := config.New()
	cfg := Config{
		GRPCAddr:   l.StringDefault("ACCOUNT_GRPC_ADDR", ":9092"),
		HTTPAddr:   l.StringDefault("ACCOUNT_HTTP_ADDR", ":8082"),
		DBDSN:      l.String("ACCOUNT_DB_DSN"),
		LedgerAddr: l.String("ACCOUNT_LEDGER_ADDR"),
	}
	return cfg, l.Err()
}
