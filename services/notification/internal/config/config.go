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
		HTTPAddr:     l.StringDefault("NOTIFICATION_HTTP_ADDR", ":8086"),
		DBDSN:        l.String("NOTIFICATION_DB_DSN"),
		KafkaBrokers: strings.Split(l.String("NOTIFICATION_KAFKA_BROKERS"), ","),
		OTLPEndpoint: l.StringDefault("NOTIFICATION_OTLP_ENDPOINT", ""),
	}
	return cfg, l.Err()
}
