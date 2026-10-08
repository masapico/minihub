package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
)

// PruneRetention selects by creation time and removes all dependent records in
// one transaction. VACUUM runs only after the deletion has committed.
func (s *Store) PruneRetention(ctx context.Context, cutoff time.Time, apply bool) (report storage.RetentionReport, err error) {
	report.Cutoff = cutoff
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TEMP TABLE retention_selected(channel_id TEXT NOT NULL,seq INTEGER NOT NULL,id TEXT NOT NULL,PRIMARY KEY(channel_id,seq))`,
		`CREATE TEMP TABLE retention_polls(id TEXT PRIMARY KEY)`,
		`CREATE TEMP TABLE retention_schedules(id TEXT PRIMARY KEY)`,
		`INSERT INTO retention_selected SELECT channel_id,seq,id FROM messages WHERE ts_ns < ?`,
	}
	for i, q := range statements {
		if i == 3 {
			_, err = tx.ExecContext(ctx, q, cutoff.UnixNano())
		} else {
			_, err = tx.ExecContext(ctx, q)
		}
		if err != nil {
			return report, fmt.Errorf("select retention records: %w", err)
		}
	}
	// An expired root takes its replies. Selecting any announcement takes its
	// complete poll or schedule, including other announcements and their replies.
	for {
		var changed int64
		for _, q := range []string{
			`INSERT OR IGNORE INTO retention_selected SELECT m.channel_id,m.seq,m.id FROM messages m JOIN retention_selected r ON r.channel_id=m.channel_id AND r.seq=m.root WHERE m.root>0`,
			`INSERT OR IGNORE INTO retention_polls SELECT m.poll_id FROM messages m JOIN retention_selected r ON r.channel_id=m.channel_id AND r.seq=m.seq WHERE m.poll_id IS NOT NULL`,
			`INSERT OR IGNORE INTO retention_schedules SELECT m.schedule_id FROM messages m JOIN retention_selected r ON r.channel_id=m.channel_id AND r.seq=m.seq WHERE m.schedule_id IS NOT NULL`,
			`INSERT OR IGNORE INTO retention_selected SELECT m.channel_id,m.seq,m.id FROM messages m WHERE m.poll_id IN (SELECT id FROM retention_polls) OR m.schedule_id IN (SELECT id FROM retention_schedules)`,
		} {
			var result sql.Result
			result, err = tx.ExecContext(ctx, q)
			if err != nil {
				return report, err
			}
			n, e := result.RowsAffected()
			if e != nil {
				return report, e
			}
			changed += n
		}
		if changed == 0 {
			break
		}
	}
	for _, item := range []struct {
		q   string
		out *int
	}{
		{`SELECT COUNT(*) FROM retention_selected`, &report.Messages},
		{`SELECT COUNT(*) FROM retention_polls`, &report.Polls},
		{`SELECT COUNT(*) FROM retention_schedules`, &report.Schedules},
	} {
		if err = tx.QueryRowContext(ctx, item.q).Scan(item.out); err != nil {
			return report, err
		}
	}
	var filesToDelete []string
	if apply {
		rows, qErr := tx.QueryContext(ctx, `SELECT attachments FROM messages WHERE EXISTS (SELECT 1 FROM retention_selected r WHERE r.channel_id=messages.channel_id AND r.seq=messages.seq) AND attachments IS NOT NULL`)
		if qErr == nil {
			defer rows.Close()
			for rows.Next() {
				var raw sql.NullString
				if rows.Scan(&raw) == nil && raw.Valid {
					var atts []domain.Attachment
					if json.Unmarshal([]byte(raw.String), &atts) == nil {
						for _, att := range atts {
							if att.Path != "" {
								filesToDelete = append(filesToDelete, att.Path)
							}
						}
					}
				}
			}
		}
		for _, q := range []string{
			`DELETE FROM mention_refs WHERE message_id IN (SELECT id FROM retention_selected)`,
			`DELETE FROM mention_reads WHERE message_id IN (SELECT id FROM retention_selected)`,
			`DELETE FROM reaction_events WHERE EXISTS (SELECT 1 FROM retention_selected r WHERE r.channel_id=reaction_events.channel_id AND r.seq=reaction_events.seq)`,
			`DELETE FROM reactions WHERE EXISTS (SELECT 1 FROM retention_selected r WHERE r.channel_id=reactions.channel_id AND r.seq=reactions.seq)`,
			`DELETE FROM withdrawals WHERE (kind='message' AND EXISTS (SELECT 1 FROM retention_selected r WHERE r.channel_id=withdrawals.channel_id AND CAST(r.seq AS TEXT)=withdrawals.target_id)) OR (kind='poll' AND target_id IN (SELECT id FROM retention_polls)) OR (kind='schedule' AND target_id IN (SELECT id FROM retention_schedules))`,
			`DELETE FROM read_state WHERE root>0 AND EXISTS (SELECT 1 FROM retention_selected r JOIN messages m ON m.channel_id=r.channel_id AND m.seq=r.seq WHERE m.root=0 AND r.channel_id=read_state.channel_id AND r.seq=read_state.root)`,
			`DELETE FROM poll_response_events WHERE poll_id IN (SELECT id FROM retention_polls)`,
			`DELETE FROM response_events WHERE schedule_id IN (SELECT id FROM retention_schedules)`,
			`DELETE FROM polls WHERE id IN (SELECT id FROM retention_polls)`,
			`DELETE FROM schedules WHERE id IN (SELECT id FROM retention_schedules)`,
			`DELETE FROM counters WHERE kind='schedule' AND id IN (SELECT id FROM retention_schedules)`,
			`DELETE FROM counters WHERE kind='poll' AND id IN (SELECT id FROM retention_polls)`,
			`DELETE FROM messages WHERE EXISTS (SELECT 1 FROM retention_selected r WHERE r.channel_id=messages.channel_id AND r.seq=messages.seq)`,
		} {
			if _, err = tx.ExecContext(ctx, q); err != nil {
				return report, err
			}
		}
	}
	for _, q := range []string{`DROP TABLE retention_selected`, `DROP TABLE retention_polls`, `DROP TABLE retention_schedules`} {
		if _, err = tx.ExecContext(ctx, q); err != nil {
			return report, err
		}
	}
	if !apply {
		return report, nil
	}
	if err = tx.Commit(); err != nil {
		return report, err
	}
	for _, f := range filesToDelete {
		_ = os.Remove(f)
	}
	if err = s.checkpointRetention(ctx); err != nil {
		return report, err
	}
	if _, err = s.write.ExecContext(ctx, `VACUUM`); err != nil {
		return report, err
	}
	return report, s.checkpointRetention(ctx)
}

func (s *Store) checkpointRetention(ctx context.Context) error {
	var busy, logFrames, checkpointed int
	if err := s.write.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("SQLite WAL checkpoint is busy")
	}
	return nil
}
