// Package config implements env-only configuration with fail-fast semantics
// (project conventions §8): every missing required variable is reported at startup.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Loader accumulates every missing/invalid variable so startup fails once
// with the full list instead of one variable at a time.
type Loader struct {
	problems []string
}

func New() *Loader { return &Loader{} }

func (l *Loader) String(key string) string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		l.problems = append(l.problems, key+" is required")
		return ""
	}
	return v
}

func (l *Loader) StringDefault(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func (l *Loader) Int(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.problems = append(l.problems, fmt.Sprintf("%s: not an integer: %q", key, v))
		return def
	}
	return n
}

func (l *Loader) Duration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.problems = append(l.problems, fmt.Sprintf("%s: not a duration: %q", key, v))
		return def
	}
	return d
}

// Err returns nil when every required variable was present and valid.
func (l *Loader) Err() error {
	if len(l.problems) == 0 {
		return nil
	}
	return fmt.Errorf("config: %s", strings.Join(l.problems, "; "))
}
