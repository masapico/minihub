package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
	"os"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"
)

// Uses the same distribution as filestore.TestThreadIndexLoadProfile.
// Generation is excluded; this measures service-time use of persistent indexes.
func TestHistoryProfile(t *testing.T) {
	raw := os.Getenv("THREAD_BENCH_MESSAGES")
	if raw == "" {
		t.Skip("set THREAD_BENCH_MESSAGES")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 100 || n > 4000000 {
		t.Fatal("invalid profile size")
	}
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO messages(channel_id,seq,id,ts,ts_ns,user_id,text,root) VALUES('general',?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i := 1; i <= n; i++ {
			root := 0
			if (i-1)%10 > 0 && (i-1)%10 < 4 {
				root = i - (i-1)%10
			}
			ts := now.Add(time.Duration(i) * time.Second)
			if _, err = stmt.ExecContext(ctx, i, fmt.Sprintf("m%d", i), ts.Format(time.RFC3339Nano), ts.UnixNano(), fmt.Sprintf("u%d", i%20), "業務連絡の確認をお願いします。スレッド返信と通常投稿の混在を測定するためのデータです。", root); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO counters VALUES('channel','general',?)", n)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Close/reopen so generation caches do not become the measured baseline.
	var path string
	if err = s.read.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	started := time.Now()
	if _, err = s.GetMessages(ctx, "general", storage.MessageQuery{Limit: 100}); err != nil {
		t.Fatal(err)
	}
	first := time.Since(started)
	runtime.GC()
	runtime.ReadMemStats(&after)
	profile := func(name string, fn func() error) {
		t.Helper()
		values := make([]time.Duration, 30)
		for i := range values {
			start := time.Now()
			if err := fn(); err != nil {
				t.Fatal(err)
			}
			values[i] = time.Since(start)
		}
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		t.Logf("%s p95=%s", name, values[28])
	}
	profile("main_page", func() error { _, err := s.GetMessages(ctx, "general", storage.MessageQuery{Limit: 100}); return err })
	profile("thread_page", func() error {
		_, err := s.GetMessages(ctx, "general", storage.MessageQuery{ThreadRootSeq: 1, Limit: 100})
		return err
	})
	profile("all_summaries", func() error { _, err := s.ThreadSummaries(ctx, "u1", "general"); return err })
	profile("append_sync", func() error {
		_, err := s.AddMessage(ctx, "general", domain.Message{UserID: "u1", ThreadRootSeq: 1, Text: "profile append"})
		return err
	})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("messages=%d first_page=%s heap_delta_MiB=%.1f db_MiB=%.1f", n, first, float64(int64(after.HeapAlloc)-int64(before.HeapAlloc))/(1<<20), float64(info.Size())/(1<<20))
}
