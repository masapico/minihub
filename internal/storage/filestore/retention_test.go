package filestore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/masapico/minihub/internal/domain"
)

func TestRetentionResumesAfterPartialFileCleanup(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChannel(ctx, &domain.Channel{ID: "chat", Name: "Chat", Type: domain.ChannelPublic}); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	old, err := s.AddMessage(ctx, "chat", domain.Message{UserID: "alice", Text: "old", Timestamp: cutoff.Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.AddMessage(ctx, "chat", domain.Message{UserID: "alice", Text: "new", Timestamp: cutoff})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.planRetention(ctx, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(ctx, filepath.Join(root, "channels", "chat", "sequence.json"), struct {
		LastSeq int64 `json:"lastSeq"`
	}{current.Seq}); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(ctx, filepath.Join(root, RetentionJournal), plan); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after rewriting the logs but before removing the journal.
	if err := s.applyRetention(ctx, plan); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	report, err := restarted.PruneRetention(ctx, cutoff, true)
	if err != nil || report.Messages != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if _, err := os.Stat(filepath.Join(root, RetentionJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains: %v", err)
	}
	if _, err := restarted.GetMessage(ctx, "chat", old.Seq); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old message: %v", err)
	}
	if _, err := restarted.GetMessage(ctx, "chat", current.Seq); err != nil {
		t.Fatal(err)
	}
	next, err := restarted.AddMessage(ctx, "chat", domain.Message{UserID: "alice", Text: "after"})
	if err != nil || next.Seq != current.Seq+1 {
		t.Fatalf("seq=%d err=%v", next.Seq, err)
	}
}
