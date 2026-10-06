package filestore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
)

func newStore(t *testing.T) *FileStorage {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDeleteGroup(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.SaveGroup(ctx, &domain.Group{ID: "sales", Name: "Sales"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteGroup(ctx, "sales"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetGroup(ctx, "sales"); !os.IsNotExist(err) {
		t.Fatalf("GetGroup error=%v, want not exist", err)
	}
	if err := s.DeleteGroup(ctx, "sales"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if err := s.DeleteGroup(ctx, "../sales"); err == nil {
		t.Fatal("unsafe group ID was accepted")
	}
}

func TestConcurrentMessagesHaveContiguousSequences(t *testing.T) {
	s := newStore(t)
	const count = 50
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.AddMessage(context.Background(), "c001", domain.Message{UserID: "u001", Text: fmt.Sprintf("message %d", i)}); err != nil {
				t.Errorf("AddMessage: %v", err)
			}
		}(i)
	}
	wg.Wait()
	page, err := s.GetMessages(context.Background(), "c001", storage.MessageQuery{Limit: count})
	msgs := page.Messages
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != count {
		t.Fatalf("got %d messages, want %d", len(msgs), count)
	}
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].Seq < msgs[j].Seq })
	for i, msg := range msgs {
		if msg.Seq != int64(i+1) {
			t.Fatalf("message %d has seq %d", i, msg.Seq)
		}
	}
}

func TestMessagePagingLatestBeforeAndAfter(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i := 1; i <= 250; i++ {
		day := 1 + (i-1)/100
		_, err := s.AddMessage(ctx, "c001", domain.Message{
			UserID: "u001", Text: fmt.Sprintf("message %d", i),
			Timestamp: time.Date(2026, 1, day, 12, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	assertPage := func(query storage.MessageQuery, first, last int64, nextBefore, nextAfter *int64) {
		t.Helper()
		page, err := s.GetMessages(ctx, "c001", query)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Messages) != int(last-first+1) || page.Messages[0].Seq != first || page.Messages[len(page.Messages)-1].Seq != last {
			t.Fatalf("page %d..%d: %#v", first, last, page.Messages)
		}
		if !sameCursor(page.NextBefore, nextBefore) || !sameCursor(page.NextAfter, nextAfter) {
			t.Fatalf("page %d..%d cursors before=%v after=%v", first, last, page.NextBefore, page.NextAfter)
		}
	}

	seq151, seq51, seq100, seq200 := int64(151), int64(51), int64(100), int64(200)
	assertPage(storage.MessageQuery{Limit: 100}, 151, 250, &seq151, nil)
	assertPage(storage.MessageQuery{BeforeSeq: &seq151, Limit: 100}, 51, 150, &seq51, nil)
	assertPage(storage.MessageQuery{BeforeSeq: &seq51, Limit: 100}, 1, 50, nil, nil)
	zero := int64(0)
	assertPage(storage.MessageQuery{AfterSeq: &zero, Limit: 100}, 1, 100, nil, &seq100)
	assertPage(storage.MessageQuery{AfterSeq: &seq100, Limit: 100}, 101, 200, nil, &seq200)
	assertPage(storage.MessageQuery{AfterSeq: &seq200, Limit: 100}, 201, 250, nil, nil)
}

func sameCursor(got, want *int64) bool {
	return got == nil && want == nil || got != nil && want != nil && *got == *want
}

func TestReactionsAreIdempotentAndRecoverAfterRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	event := domain.ReactionEvent{MessageSeq: 7, UserID: "u001", Key: "ack", Active: true}
	changed, err := store.SetReaction(ctx, "c001", event)
	if err != nil || !changed {
		t.Fatalf("first SetReaction changed=%v err=%v", changed, err)
	}
	changed, err = store.SetReaction(ctx, "c001", event)
	if err != nil || changed {
		t.Fatalf("duplicate SetReaction changed=%v err=%v", changed, err)
	}

	restarted, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	states, err := restarted.GetReactions(ctx, "c001", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].MessageSeq != 7 || states[0].Key != "ack" || len(states[0].UserIDs) != 1 || states[0].UserIDs[0] != "u001" {
		t.Fatalf("unexpected recovered reactions: %#v", states)
	}
	userIDs, err := restarted.GetReactionUsers(ctx, "c001", 7, "ack")
	if err != nil || len(userIDs) != 1 || userIDs[0] != "u001" {
		t.Fatalf("reaction users=%#v err=%v", userIDs, err)
	}
	changed, err = restarted.SetReaction(ctx, "c001", domain.ReactionEvent{MessageSeq: 7, UserID: "u001", Key: "ack", Active: false})
	if err != nil || !changed {
		t.Fatalf("remove changed=%v err=%v", changed, err)
	}
	states, err = restarted.GetReactions(ctx, "c001", 0)
	if err != nil || len(states) != 0 {
		t.Fatalf("removed reactions=%#v err=%v", states, err)
	}
	userIDs, err = restarted.GetReactionUsers(ctx, "c001", 7, "ack")
	if err != nil || len(userIDs) != 0 {
		t.Fatalf("removed reaction users=%#v err=%v", userIDs, err)
	}
}

func TestReactionRecoveryIgnoresAndTruncatesIncompleteTail(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetReaction(ctx, "c001", domain.ReactionEvent{MessageSeq: 1, UserID: "u001", Key: "ack", Active: true}); err != nil {
		t.Fatal(err)
	}
	files, err := store.reactionFiles("c001")
	if err != nil || len(files) != 1 {
		t.Fatalf("reaction files=%v err=%v", files, err)
	}
	f, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"version":1,"messageSeq":`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	states, err := restarted.GetReactions(ctx, "c001", 0)
	if err != nil || len(states) != 1 {
		t.Fatalf("recovered states=%#v err=%v", states, err)
	}
	if _, err := restarted.SetReaction(ctx, "c001", domain.ReactionEvent{MessageSeq: 1, UserID: "u001", Key: "ack", Active: false}); err != nil {
		t.Fatal(err)
	}
	again, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	states, err = again.GetReactions(ctx, "c001", 0)
	if err != nil || len(states) != 0 {
		t.Fatalf("states after truncation=%#v err=%v", states, err)
	}
}

func TestReadStateOnlyAdvances(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, seq := range []int64{10, 4, 12, 9} {
		if err := s.SetReadState(ctx, "u001", "c001", seq); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetReadState(ctx, "u001", "c001")
	if err != nil {
		t.Fatal(err)
	}
	if got != 12 {
		t.Fatalf("got %d, want 12", got)
	}
	if err := s.SetReadState(ctx, "u001", "c002", 7); err != nil {
		t.Fatal(err)
	}
	states, err := s.GetReadStates(ctx, "u001")
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states["c001"] != 12 || states["c002"] != 7 {
		t.Fatalf("states=%v", states)
	}
}

func TestMentionIndexUsesRecipientAndLegacyReferences(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, user := range []domain.User{
		{ID: "alice", Name: "Alice", Groups: []string{"sales"}, Role: domain.RoleUser, Enabled: true},
		{ID: "bob", Name: "Bob", Role: domain.RoleUser, Enabled: true},
	} {
		user := user
		if err := s.SaveUser(ctx, &user); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveChannel(ctx, &domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic}); err != nil {
		t.Fatal(err)
	}
	aliceMessage, err := s.AddMessage(ctx, "general", domain.Message{UserID: "bob", Text: "@alice hello", MentionUserIDs: []string{"alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(ctx, "general", domain.Message{UserID: "alice", Text: "@bob hello", MentionUserIDs: []string{"bob"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(ctx, "general", domain.Message{UserID: "bob", Text: "@group:sales legacy"}); err != nil {
		t.Fatal(err)
	}

	mentions, err := s.ListMentions(ctx, "alice")
	if err != nil || len(mentions) != 2 {
		t.Fatalf("alice mentions=%v err=%v", mentions, err)
	}
	if len(s.mentionRecords) != 3 || len(s.mentionUserRefs["alice"]) != 1 || len(s.mentionUserRefs["bob"]) != 1 || len(s.legacyGroupRefs["sales"]) != 1 {
		t.Fatalf("records=%d alice=%v bob=%v sales=%v", len(s.mentionRecords), s.mentionUserRefs["alice"], s.mentionUserRefs["bob"], s.legacyGroupRefs["sales"])
	}
	alice, err := s.GetUser(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	alice.Groups = nil
	if err := s.SaveUser(ctx, alice); err != nil {
		t.Fatal(err)
	}
	mentions, err = s.ListMentions(ctx, "alice")
	if err != nil || len(mentions) != 1 || mentions[0].Message.ID != aliceMessage.ID {
		t.Fatalf("alice mentions after group change=%v err=%v", mentions, err)
	}
	if _, err := s.AddMessage(ctx, "general", domain.Message{UserID: "bob", Text: "again", MentionUserIDs: []string{"alice", "alice"}}); err != nil {
		t.Fatal(err)
	}
	mentions, err = s.ListMentions(ctx, "alice")
	if err != nil || len(mentions) != 2 {
		t.Fatalf("alice mentions after indexed append=%v err=%v", mentions, err)
	}
}

func TestMentionIndexConcurrentLoadAndAppend(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.SaveUser(ctx, &domain.User{ID: "alice", Name: "Alice", Role: domain.RoleUser, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUser(ctx, &domain.User{ID: "bob", Name: "Bob", Role: domain.RoleUser, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChannel(ctx, &domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := s.AddMessage(ctx, "general", domain.Message{UserID: "bob", Text: fmt.Sprintf("seed %d", i), MentionUserIDs: []string{"alice"}}); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	posted := make(chan domain.Message, 1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, err := s.ListMentions(ctx, "alice")
		errs <- err
	}()
	go func() {
		defer wg.Done()
		<-start
		message, err := s.AddMessage(ctx, "general", domain.Message{UserID: "bob", Text: "concurrent", MentionUserIDs: []string{"alice"}})
		if err == nil {
			posted <- message
		}
		errs <- err
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	message := <-posted
	mentions, err := s.ListMentions(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	occurrences := 0
	for _, mention := range mentions {
		if mention.Message.ID == message.ID {
			occurrences++
		}
	}
	if len(mentions) != 11 || occurrences != 1 {
		t.Fatalf("mentions=%d concurrent occurrences=%d", len(mentions), occurrences)
	}
}

func TestRecoveryIgnoresIncompleteTail(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.AddMessage(ctx, "c001", domain.Message{UserID: "u001", Text: "complete"}); err != nil {
		t.Fatal(err)
	}
	files, err := s.messageFiles("c001")
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	f, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"seq":2,"id":"broken"`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := s.GetMessages(ctx, "c001", storage.MessageQuery{Limit: 100})
	msgs := page.Messages
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}

	// A restarted process can safely append after the crash tail. The partial
	// bytes are removed before the next committed JSONL record is written.
	restarted, err := New(s.root)
	if err != nil {
		t.Fatal(err)
	}
	appended, err := restarted.AddMessage(ctx, "c001", domain.Message{UserID: "u001", Text: "after restart"})
	if err != nil {
		t.Fatal(err)
	}
	if appended.Seq != 2 {
		t.Fatalf("appended seq=%d, want 2", appended.Seq)
	}
	page, err = restarted.GetMessages(ctx, "c001", storage.MessageQuery{Limit: 100})
	msgs = page.Messages
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d recovered messages, want 2", len(msgs))
	}
}

func TestMalformedCompleteLineIsCorruption(t *testing.T) {
	s := newStore(t)
	dir := filepath.Join(s.root, "channels", "c001", "messages")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-01-01.jsonl"), []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMessages(context.Background(), "c001", storage.MessageQuery{Limit: 100}); err == nil {
		t.Fatal("expected corruption error")
	}
}

func TestRejectsPathTraversalID(t *testing.T) {
	s := newStore(t)
	if err := s.SaveUser(context.Background(), &domain.User{ID: "../escape"}); err == nil {
		t.Fatal("expected invalid ID error")
	}
}
