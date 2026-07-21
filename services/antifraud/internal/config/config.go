package config

import (
	"strings"

	"github.com/aidostt/bank-core/pkg/config"
)

type Config struct {
	HTTPAddr     string
	DBDSN        string
	KafkaBrokers []string
	OTLPEndpoint string
}

func Load() (Config, error) {
	l := config.New()
	cfg := Config{
		HTTPAddr:     l.StringDefault("ANTIFRAUD_HTTP_ADDR", ":8085"),
		DBDSN:        l.String("ANTIFRAUD_DB_DSN"),
		KafkaBrokers: strings.Split(l.String("ANTIFRAUD_KAFKA_BROKERS"), ","),
		OTLPEndpoint: l.StringDefault("ANTIFRAUD_OTLP_ENDPOINT", ""),
	}
	return cfg, l.Err()
}
