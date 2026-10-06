package filestore

import (
	"context"
	"fmt"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestThreadIndexesPagingRecoveryAndReadState(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	root, err := s.AddMessage(ctx, "general", domain.Message{UserID: "alice", Text: "root"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		if _, err := s.AddMessage(ctx, "general", domain.Message{UserID: "bob", Text: fmt.Sprint(i), ThreadRootSeq: root.Seq}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddMessage(ctx, "general", domain.Message{UserID: "alice", Text: "main"}); err != nil {
			t.Fatal(err)
		}
	}
	latest, count, err := s.MessageReadCounts(ctx, "general", 1)
	if err != nil || latest != 51 || count != 25 {
		t.Fatalf("latest/count=%d/%d err=%v", latest, count, err)
	}
	page, err := s.GetMessages(ctx, "general", storage.MessageQuery{Limit: 10})
	if err != nil || len(page.Messages) != 10 || page.Messages[0].Seq != 33 {
		t.Fatalf("main=%+v err=%v", page, err)
	}
	zero := int64(0)
	page, err = s.GetMessages(ctx, "general", storage.MessageQuery{ThreadRootSeq: root.Seq, AfterSeq: &zero, Limit: 10})
	if err != nil || len(page.Messages) != 10 || page.Messages[0].Seq != 2 || *page.NextAfter != 20 {
		t.Fatalf("thread=%+v err=%v", page, err)
	}
	around := int64(30)
	page, err = s.GetMessages(ctx, "general", storage.MessageQuery{ThreadRootSeq: 1, AroundSeq: &around, Limit: 5})
	if err != nil || len(page.Messages) != 5 || page.Messages[2].Seq != 30 || page.NextBefore == nil || page.NextAfter == nil {
		t.Fatalf("around=%+v err=%v", page, err)
	}
	if err := s.SetThreadReadState(ctx, "alice", "general", 1, 20); err != nil {
		t.Fatal(err)
	}
	if err := s.SetReadState(ctx, "alice", "general", 51); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadReadState(ctx, "alice", "general", 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMentionRead(ctx, "alice", "some-message", root.Timestamp); err != nil {
		t.Fatal(err)
	}
	summaries, err := s.ThreadSummaries(ctx, "alice", "general")
	if err != nil || len(summaries) != 1 || summaries[0].UnreadCount != 15 || summaries[0].LastReadSeq != 20 || !summaries[0].Participating {
		t.Fatalf("summaries=%+v err=%v", summaries, err)
	}
	own, err := s.ThreadSummaries(ctx, "bob", "general")
	if err != nil || own[0].UnreadCount != 0 || !own[0].Participating {
		t.Fatalf("own=%+v %v", own, err)
	}
	if err := s.SetThreadReadState(ctx, "alice", "general", 1, 3); err == nil {
		t.Fatal("main message accepted as thread read position")
	}
	files, _ := s.messageFiles("general")
	f, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"seq":52,"text":"partial`)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(s.root)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := restarted.AddMessage(ctx, "general", domain.Message{UserID: "bob", Text: "recovered", ThreadRootSeq: 1})
	if err != nil || reply.Seq != 52 {
		t.Fatalf("recovery=%+v %v", reply, err)
	}
	summaries, err = restarted.ThreadSummaries(ctx, "alice", "general")
	if err != nil || summaries[0].ReplyCount != 26 || summaries[0].UnreadCount != 16 {
		t.Fatalf("restarted=%+v %v", summaries, err)
	}
}
func TestThreadConcurrentWritesAndStateMerge(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	root, err := s.AddMessage(ctx, "general", domain.Message{UserID: "alice", Text: "root"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m := domain.Message{UserID: "bob", Text: "message"}
			if i%2 == 0 {
				m.ThreadRootSeq = root.Seq
			}
			if _, err := s.AddMessage(ctx, "general", m); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	all := map[int64]bool{}
	for _, r := range []int64{0, root.Seq} {
		page, err := s.GetMessages(ctx, "general", storage.MessageQuery{ThreadRootSeq: r, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page.Messages {
			if all[m.Seq] {
				t.Fatal("duplicate sequence")
			}
			all[m.Seq] = true
		}
	}
	for i := int64(1); i <= 51; i++ {
		if !all[i] {
			t.Fatalf("missing %d", i)
		}
	}
	page, _ := s.GetMessages(ctx, "general", storage.MessageQuery{ThreadRootSeq: root.Seq, Limit: 100})
	for _, m := range page.Messages {
		wg.Add(1)
		go func(seq int64) {
			defer wg.Done()
			if err := s.SetThreadReadState(ctx, "alice", "general", root.Seq, seq); err != nil {
				t.Error(err)
			}
		}(m.Seq)
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := s.SetReadState(ctx, "alice", "general", 1); err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := s.SetMentionRead(ctx, "alice", "notice", root.Timestamp); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	var state userState
	if err := readJSON(ctx, filepath.Join(s.root, "state", "alice.json"), &state); err != nil {
		t.Fatal(err)
	}
	if state.Channels["general"].LastReadSeq != 1 || state.Threads["general"][1].LastReadSeq != page.Messages[len(page.Messages)-1].Seq || len(state.Mentions) != 1 {
		t.Fatalf("state=%+v", state)
	}
}
func TestThreadPositionIndexDoesNotReadUnrequestedBodies(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	root, _ := s.AddMessage(ctx, "general", domain.Message{UserID: "alice", Text: "root"})
	reply, err := s.AddMessage(ctx, "general", domain.Message{UserID: "bob", Text: "reply", ThreadRootSeq: root.Seq})
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt only the indexed reply body after warm-up. Main history must not read it.
	pos := s.positionIndexes["general"].bySeq[reply.Seq]
	f, err := os.OpenFile(pos.path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteAt([]byte("!"), pos.offset); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := s.GetMessages(ctx, "general", storage.MessageQuery{Limit: 100})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("main read a reply: %+v %v", page, err)
	}
	if _, err := s.GetMessages(ctx, "general", storage.MessageQuery{ThreadRootSeq: root.Seq, Limit: 50}); err == nil {
		t.Fatal("reply corruption was ignored")
	}
	restarted, _ := New(s.root)
	if _, err := restarted.GetMessages(ctx, "general", storage.MessageQuery{Limit: 100}); err == nil {
		t.Fatal("cold index accepted corrupt log")
	}
	if _, err := restarted.AddMessage(ctx, "unrelated", domain.Message{UserID: "alice", Text: "ok"}); err != nil {
		t.Fatal(err)
	}
}
