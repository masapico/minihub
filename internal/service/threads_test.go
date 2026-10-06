package service_test

import (
	"context"
	"errors"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/service"
	"github.com/masapico/minihub/internal/storage"
	"testing"
)

func (p *recordingPublisher) ThreadUpdated(_ context.Context, channel string, root, seq int64, userID string) {
	p.channelID = channel
	p.reactionSeq = root
	p.seq = seq
}
func TestThreadsAuthorizationUnreadAndMentions(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	pub := &recordingPublisher{}
	svc.SetMessagePublisher(pub)
	_, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic, Members: []string{"alice"}})
	if err != nil {
		t.Fatal(err)
	}
	root, err := svc.PostMessage(ctx, "alice", "general", "Question")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostThreadMessage(ctx, "bob", "general", root.Seq, "no membership"); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("unjoined=%v", err)
	}
	if _, err := svc.GetThreadMessages(ctx, "bob", "general", root.Seq, storage.MessageQuery{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.JoinChannel(ctx, "bob", "general"); err != nil {
		t.Fatal(err)
	}
	reply, err := svc.PostThreadMessage(ctx, "bob", "general", root.Seq, "@alice answer")
	if err != nil {
		t.Fatal(err)
	}
	if pub.seq != reply.Seq || pub.reactionSeq != root.Seq {
		t.Fatalf("event=%+v", pub)
	}
	status, err := svc.GetReadStatus(ctx, "bob", "general")
	if err != nil || status.LastReadSeq != root.Seq || status.UnreadCount != 0 || status.LatestSeq != root.Seq || status.ThreadUpdates {
		t.Fatalf("reply advanced channel read: %+v %v", status, err)
	}
	status, err = svc.GetReadStatus(ctx, "alice", "general")
	if err != nil || status.UnreadCount != 0 || !status.ThreadUpdates {
		t.Fatalf("alice=%+v %v", status, err)
	}
	page, err := svc.GetMessages(ctx, "alice", "general", storage.MessageQuery{})
	if err != nil || len(page.Messages) != 1 || page.Threads[root.Seq].ReplyCount != 1 {
		t.Fatalf("main=%+v %v", page, err)
	}
	list, err := svc.ListThreads(ctx, "alice", "", true, true, "", 1)
	if err != nil || len(list.Threads) != 1 || list.UnreadCount != 1 {
		t.Fatalf("list=%+v %v", list, err)
	}
	mentions, err := svc.ListMentions(ctx, "alice", "", 50)
	if err != nil || len(mentions.Mentions) != 1 || mentions.Mentions[0].Message.ThreadRootSeq != root.Seq {
		t.Fatalf("mentions=%+v %v", mentions, err)
	}
	if _, err := svc.PostThreadMessage(ctx, "bob", "general", reply.Seq, "nested"); !errors.Is(err, service.ErrInvalid) {
		t.Fatalf("nested=%v", err)
	}
	if _, err := svc.PostThreadMessage(ctx, "bob", "general", 9999, "missing"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("missing=%v", err)
	}
	if err := svc.MarkRead(ctx, "alice", "general", reply.Seq); !errors.Is(err, service.ErrInvalid) {
		t.Fatalf("main read accepted reply=%v", err)
	}
	if err := svc.MarkThreadRead(ctx, "alice", "general", root.Seq, root.Seq); !errors.Is(err, service.ErrInvalid) {
		t.Fatalf("thread read accepted root=%v", err)
	}
	if err := svc.MarkThreadRead(ctx, "alice", "general", root.Seq, reply.Seq); err != nil {
		t.Fatal(err)
	}
	mentions, _ = svc.ListMentions(ctx, "alice", "", 50)
	if mentions.UnreadCount != 1 {
		t.Fatal("thread read cleared mention")
	}
	_, err = svc.CreateChannel(ctx, "admin", domain.Channel{ID: "secret", Name: "Secret", Type: domain.ChannelPrivate, Members: []string{"alice"}})
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := svc.PostMessage(ctx, "alice", "secret", "private")
	if _, err := svc.PostThreadMessage(ctx, "admin", "secret", secret.Seq, "private reply"); err != nil {
		t.Fatal(err)
	}
	for _, call := range []func() error{
		func() error {
			_, err := svc.GetThreadMessages(ctx, "bob", "secret", secret.Seq, storage.MessageQuery{})
			return err
		},
		func() error { _, err := svc.PostThreadMessage(ctx, "bob", "secret", secret.Seq, "hidden"); return err },
		func() error { _, err := svc.ListThreads(ctx, "bob", "secret", false, false, "", 50); return err },
		func() error { return svc.MarkThreadRead(ctx, "bob", "secret", secret.Seq, 2) },
	} {
		if err := call(); !errors.Is(err, service.ErrNotFound) {
			t.Fatalf("private access=%v", err)
		}
	}
	list, err = svc.ListThreads(ctx, "bob", "", false, false, "", 50)
	if err != nil || len(list.Threads) != 1 || list.Threads[0].ChannelID != "general" {
		t.Fatalf("leaked private list=%+v %v", list, err)
	}
	if err := svc.ArchiveChannel(ctx, "admin", "general"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostThreadMessage(ctx, "alice", "general", root.Seq, "archived"); err == nil {
		t.Fatal("archived write")
	}
}
func TestThreadListCursorAndDefaultUnreadPage(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	_, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic, Members: []string{"alice", "bob"}})
	if err != nil {
		t.Fatal(err)
	}
	root, _ := svc.PostMessage(ctx, "alice", "general", "first")
	for i := 0; i < 60; i++ {
		if _, err := svc.PostThreadMessage(ctx, "bob", "general", root.Seq, "reply"); err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.GetThreadMessages(ctx, "alice", "general", root.Seq, storage.MessageQuery{Limit: 10})
	if err != nil || page.Messages[0].Seq != 2 || page.NextAfter == nil {
		t.Fatalf("first unread=%+v %v", page, err)
	}
	second, _ := svc.PostMessage(ctx, "alice", "general", "second")
	if _, err := svc.PostThreadMessage(ctx, "bob", "general", second.Seq, "reply"); err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListThreads(ctx, "alice", "", true, true, "", 1)
	if err != nil || len(list.Threads) != 1 || list.Threads[0].RootSeq != second.Seq || list.NextCursor == "" {
		t.Fatalf("list=%+v %v", list, err)
	}
	next, err := svc.ListThreads(ctx, "alice", "", true, true, list.NextCursor, 1)
	if err != nil || len(next.Threads) != 1 || next.Threads[0].RootSeq != root.Seq {
		t.Fatalf("next=%+v %v", next, err)
	}
}
