package catalogsync

import (
	"context"
	"fmt"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/logging"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// BackfillBatchSize is how many catalog_item rows one backfill transaction
// covers. One commit per batch instead of one per row is what makes a
// 50k-row upgrade take seconds instead of minutes (every autocommit statement
// is its own fsync'd transaction).
const BackfillBatchSize = 1000

// backfillLogEvery is the minimum gap between two progress log lines.
const backfillLogEvery = 5 * time.Second

// backfill describes one startup backfill over catalog_item: the rows still
// missing derived data (where), the columns to read (cols), and how to fix
// one row (apply, running inside the batch's transaction).
type backfill[R any] struct {
	name  string
	where string
	cols  string
	id    func(R) string
	apply func(ctx context.Context, tx storage.DB, r R) error
}

// runBackfill walks the matching rows in id order, BackfillBatchSize at a
// time, one transaction per batch, logging progress (rows done/total, at
// most every backfillLogEvery). It is resumable and idempotent: a crash or a
// cancelled context loses at most the batch in flight, and the next run only
// sees rows still matching where. Keyset pagination (id > last) rather than
// re-running the query from the top guarantees termination even for rows
// that keep matching after apply (e.g. an item whose genres column holds no
// names).
func runBackfill[R any](ctx context.Context, db storage.DB, b backfill[R]) error {
	var total []int
	if err := db.Query(ctx, &total, "SELECT COUNT(*) FROM catalog_item WHERE "+b.where); err != nil {
		return fmt.Errorf("catalogsync: count %s backfill rows: %w", b.name, err)
	}
	if len(total) == 0 || total[0] == 0 {
		return nil
	}
	start := time.Now()
	lastLog := start
	logging.Infof("catalogsync: backfilling %s for %d catalog items", b.name, total[0])
	done, last := 0, ""
	for {
		var rows []R
		if err := db.Query(ctx, &rows,
			"SELECT "+b.cols+" FROM catalog_item WHERE ("+b.where+") AND id > ? ORDER BY id LIMIT ?",
			last, BackfillBatchSize,
		); err != nil {
			return fmt.Errorf("catalogsync: list %s backfill rows: %w", b.name, err)
		}
		if len(rows) == 0 {
			break
		}
		err := db.Transact(ctx, func(tx storage.DB) error {
			for _, r := range rows {
				if err := b.apply(ctx, tx, r); err != nil {
					return fmt.Errorf("%s: %w", b.id(r), err)
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("catalogsync: backfill %s: %w", b.name, err)
		}
		done += len(rows)
		last = b.id(rows[len(rows)-1])
		if now := time.Now(); now.Sub(lastLog) >= backfillLogEvery {
			lastLog = now
			logging.Infof("catalogsync: backfilling %s: %d/%d", b.name, done, total[0])
		}
	}
	logging.Infof("catalogsync: backfilled %s for %d items in %s", b.name, done, time.Since(start).Round(time.Millisecond))
	return nil
}
