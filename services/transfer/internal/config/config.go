package config

import (
	"strings"
	"time"

	"github.com/aidostt/bank-core/pkg/config"
)

type Config struct {
	GRPCAddr           string
	HTTPAddr           string
	DBDSN              string
	KafkaBrokers       []string
	LedgerAddr         string
	AccountAddr        string
	RecoveryInterval   time.Duration
	RecoveryStaleAfter time.Duration
}

func Load() (Config, error) {
	l := config.New()
	cfg := Config{
		GRPCAddr:           l.StringDefault("TRANSFER_GRPC_ADDR", ":9094"),
		HTTPAddr:           l.StringDefault("TRANSFER_HTTP_ADDR", ":8084"),
		DBDSN:              l.String("TRANSFER_DB_DSN"),
		KafkaBrokers:       strings.Split(l.String("TRANSFER_KAFKA_BROKERS"), ","),
		LedgerAddr:         l.String("TRANSFER_LEDGER_ADDR"),
		AccountAddr:        l.String("TRANSFER_ACCOUNT_ADDR"),
		RecoveryInterval:   l.Duration("TRANSFER_RECOVERY_INTERVAL", 5*time.Second),
		RecoveryStaleAfter: l.Duration("TRANSFER_RECOVERY_STALE_AFTER", 15*time.Second),
	}
	return cfg, l.Err()
}
