package filestore

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
)

// Opt in: THREAD_BENCH_MESSAGES=1000000 go test ./internal/storage/filestore -run TestThreadIndexLoadProfile -count=1 -v
// Uses synthetic logs in t.TempDir, and reports measurements rather than machine-specific assertions.
func TestThreadIndexLoadProfile(t *testing.T) {
	raw := os.Getenv("THREAD_BENCH_MESSAGES")
	if raw == "" {
		t.Skip("set THREAD_BENCH_MESSAGES for the load profile")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 100 || n > 4000000 {
		t.Fatal("THREAD_BENCH_MESSAGES must be 100..4000000")
	}
	dir := t.TempDir()
	logs := filepath.Join(dir, "channels", "general", "messages")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(logs, "2026-09-16.jsonl"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriterSize(f, 1<<20)
	encoder := json.NewEncoder(writer)
	now := time.Now()
	for i := 1; i <= n; i++ {
		root := int64(0)
		if (i-1)%10 > 0 && (i-1)%10 < 4 {
			root = int64(i - (i-1)%10)
		}
		m := domain.Message{Seq: int64(i), ID: fmt.Sprintf("m%d", i), UserID: fmt.Sprintf("u%d", i%20), Timestamp: now.Add(time.Duration(i) * time.Second), ThreadRootSeq: root, Text: "業務連絡の確認をお願いします。スレッド返信と通常投稿の混在を測定するためのデータです。"}
		if err := encoder.Encode(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	start := time.Now()
	if _, err := s.GetMessages(ctx, "general", storage.MessageQuery{Limit: 100}); err != nil {
		t.Fatal(err)
	}
	cold := time.Since(start)
	runtime.GC()
	runtime.ReadMemStats(&after)
	measure := func(label string, call func() error) {
		t.Helper()
		times := make([]time.Duration, 30)
		for i := range times {
			start := time.Now()
			if err := call(); err != nil {
				t.Fatal(err)
			}
			times[i] = time.Since(start)
		}
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		t.Logf("%s p50=%s p95=%s", label, times[len(times)/2], times[len(times)*95/100])
	}
	t.Logf("messages=%d cold_index=%s heap_delta=%.1f MiB", n, cold, float64(after.HeapAlloc-before.HeapAlloc)/(1<<20))
	measure("main_100", func() error { _, err := s.GetMessages(ctx, "general", storage.MessageQuery{Limit: 100}); return err })
	measure("thread_page", func() error {
		_, err := s.GetMessages(ctx, "general", storage.MessageQuery{ThreadRootSeq: 1, Limit: 50})
		return err
	})
	measure("all_summaries", func() error { _, err := s.ThreadSummaries(ctx, "u1", "general"); return err })
	measure("reply_commit", func() error {
		_, err := s.AddMessage(ctx, "general", domain.Message{UserID: "u2", Text: "new reply", ThreadRootSeq: 1})
		return err
	})
	runtime.KeepAlive(s)
}
