package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/aidostt/bank-core/services/ledger/internal/domain"
)

// Sweeper releases holds past expires_at — protection against an
// orchestrator dying mid-saga (ledger doc, Failure & ops).
type Sweeper struct {
	svc      *Service
	interval time.Duration
	log      *slog.Logger
}

func NewSweeper(svc *Service, interval time.Duration, log *slog.Logger) *Sweeper {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Sweeper{svc: svc, interval: interval, log: log}
}

func (s *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep(ctx)
		}
	}
}

func (s *Sweeper) sweep(ctx context.Context) {
	holds, err := s.svc.store.ListExpiredHolds(ctx, 100)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("hold sweep scan failed", slog.String("error", err.Error()))
		}
		return
	}
	for _, h := range holds {
		// Re-checked under lock inside ReleaseHold; races with a concurrent
		// capture lose cleanly (captured → ErrHoldNotActive, skip).
		if _, err := s.svc.ReleaseHold(ctx, h.ID); err != nil {
			if !errors.Is(err, domain.ErrHoldNotActive) && ctx.Err() == nil {
				s.log.Warn("expired hold release failed",
					slog.String("hold.id", h.ID), slog.String("error", err.Error()))
			}
			continue
		}
		s.log.Warn("expired hold released — orchestrator likely died mid-saga",
			slog.String("hold.id", h.ID),
			slog.String("reference", h.ReferenceType+"/"+h.ReferenceID))
	}
}
