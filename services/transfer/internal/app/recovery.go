package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/aidostt/bank-core/services/transfer/internal/domain"
)

// Recovery re-drives stuck sagas (transfer doc): every interval it claims
// transfers sitting in HELD/POSTING/RELEASING longer than staleAfter (FOR
// UPDATE SKIP LOCKED + attempt bump makes this safe across instances),
// disambiguates POSTING via GetTransactionByReference, and fails transfers
// over the retry budget with reason=recovery_exhausted.
//
// RELEASING is included beyond the doc's HELD/POSTING list so a crash
// during compensation cannot strand a transfer (noted in the service doc).
type Recovery struct {
	svc         *Service
	interval    time.Duration
	staleAfter  time.Duration
	maxAttempts int32
	log         *slog.Logger
}

func NewRecovery(svc *Service, interval, staleAfter time.Duration, log *slog.Logger) *Recovery {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if staleAfter <= 0 {
		staleAfter = 15 * time.Second
	}
	return &Recovery{svc: svc, interval: interval, staleAfter: staleAfter, maxAttempts: 5, log: log}
}

func (r *Recovery) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.RunOnce(ctx)
		}
	}
}

// RunOnce processes one claim batch (exported for deterministic tests).
func (r *Recovery) RunOnce(ctx context.Context) {
	states := []string{string(domain.StateHeld), string(domain.StatePosting), string(domain.StateReleasing)}
	ids, err := r.svc.store.ClaimStuck(ctx, states, r.staleAfter, 10)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Warn("recovery claim failed", slog.String("error", err.Error()))
		}
		return
	}
	for _, id := range ids {
		outcome := r.recoverOne(ctx, id)
		r.log.Info("transfer recovery",
			slog.String("transfer.id", id), slog.String("outcome", outcome))
	}
}

func (r *Recovery) recoverOne(ctx context.Context, id string) string {
	t, err := r.svc.store.GetTransfer(ctx, id)
	if err != nil {
		return "load_failed"
	}

	// POSTING is ambiguous: ask the ledger whether the entry exists before
	// anything else — money may already have moved.
	if t.State == domain.StatePosting {
		exists, err := r.svc.ledger.TransactionExists(ctx, t.ID)
		if err == nil && exists {
			if terr := r.svc.transition(ctx, t, domain.EvPosted, nil,
				map[string]any{"resolved_by": "recovery"}); terr != nil && !errors.Is(terr, ErrStateRaced) {
				return "transition_failed"
			}
			return "completed_entry_found"
		}
		// err != nil → ledger still unreachable; fall through to budget check.
	}

	if t.RecoveryAttempts > r.maxAttempts && t.State != domain.StateReleasing {
		reason := "recovery_exhausted"
		if terr := r.svc.transition(ctx, t, domain.EvRecoveryExhausted,
			func(t *Transfer) { t.Reason = &reason },
			map[string]any{"attempts": t.RecoveryAttempts}); terr != nil && !errors.Is(terr, ErrStateRaced) {
			return "transition_failed"
		}
		if _, err := r.svc.Drive(ctx, t.ID); err != nil {
			return "release_pending"
		}
		return "failed_exhausted"
	}

	final, err := r.svc.Drive(ctx, t.ID)
	if err != nil {
		return "drive_failed"
	}
	return "state_" + string(final.State)
}
