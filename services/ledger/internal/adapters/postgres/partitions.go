package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EnsurePartitions creates monthly partitions for the current and next
// month (ADR-0017) and attaches the zero-sum constraint trigger to every
// new postings partition. Runs at startup and daily.
func EnsurePartitions(ctx context.Context, pool *pgxpool.Pool, now time.Time) error {
	months := []time.Time{
		time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC),
		time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0),
	}
	for _, m := range months {
		from := m.Format("2006-01-02")
		to := m.AddDate(0, 1, 0).Format("2006-01-02")
		suffix := m.Format("y2006m01")

		for _, parent := range []string{"journal_entries", "postings"} {
			part := fmt.Sprintf("%s_%s", parent, suffix)
			ddl := fmt.Sprintf(
				`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
				part, parent, from, to)
			if _, err := pool.Exec(ctx, ddl); err != nil {
				return fmt.Errorf("create partition %s: %w", part, err)
			}
		}

		// Constraint triggers cannot use IF NOT EXISTS — probe pg_trigger.
		part := "postings_" + suffix
		trigger := "trg_zero_sum_" + suffix
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = $1 AND tgrelid = $2::regclass)`,
			trigger, part).Scan(&exists)
		if err != nil {
			return fmt.Errorf("probe trigger %s: %w", trigger, err)
		}
		if !exists {
			ddl := fmt.Sprintf(
				`CREATE CONSTRAINT TRIGGER %s AFTER INSERT ON %s
				 DEFERRABLE INITIALLY DEFERRED
				 FOR EACH ROW EXECUTE FUNCTION check_entry_zero_sum()`,
				trigger, part)
			if _, err := pool.Exec(ctx, ddl); err != nil {
				return fmt.Errorf("create trigger %s: %w", trigger, err)
			}
		}
	}
	return nil
}

// RunPartitionMaintenance keeps partitions ahead of the calendar.
func RunPartitionMaintenance(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := EnsurePartitions(ctx, pool, time.Now().UTC()); err != nil && ctx.Err() == nil {
				log.Error("partition maintenance failed", slog.String("error", err.Error()))
			}
		}
	}
}
