package sqlitestore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/masapico/minihub/internal/domain"
)

func TestUpgradeV3AIColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m, err := s.AddMessage(ctx, "c", domain.Message{UserID: "u", Text: "old message"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.write.Exec("ALTER TABLE messages DROP COLUMN ai; PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, err := s.GetMessage(ctx, "c", m.Seq)
	if err != nil || old.Text != m.Text || old.AI != nil {
		t.Fatalf("old message: %#v %v", old, err)
	}
	answer, err := s.AddMessage(ctx, "c", domain.Message{UserID: "ai_helper", Text: "answer", ThreadRootSeq: m.Seq, AI: &domain.AIMessage{ID: "helper", Name: "AI"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMessage(ctx, "c", answer.Seq)
	if err != nil || got.AI == nil || got.AI.Name != "AI" {
		t.Fatalf("AI message: %#v %v", got, err)
	}
}
