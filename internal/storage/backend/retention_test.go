package backend

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/masapico/minihub/internal/domain"
)

func TestRetentionBothBackends(t *testing.T) {
	zone := time.FixedZone("JST", 9*60*60)
	cutoff := time.Date(2025, 1, 1, 0, 0, 0, 0, zone)
	for _, kind := range []string{"file", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			s, err := Open(root, kind)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Prune(ctx, root, kind, cutoff, true); err == nil {
				t.Fatal("retention ran while server lock was held")
			}
			for _, user := range []string{"alice", "bob"} {
				if err := s.SaveUser(ctx, &domain.User{ID: user, Name: user, Enabled: true}); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.SaveChannel(ctx, &domain.Channel{ID: "chat", Name: "Chat", Type: domain.ChannelPublic}); err != nil {
				t.Fatal(err)
			}
			old := cutoff.Add(-time.Nanosecond)
			fresh := cutoff
			rootMessage, err := s.AddMessage(ctx, "chat", domain.Message{UserID: "alice", Text: "old root", Timestamp: old, MentionUserIDs: []string{"bob"}})
			if err != nil {
				t.Fatal(err)
			}
			reply, err := s.AddMessage(ctx, "chat", domain.Message{UserID: "bob", Text: "new reply", Timestamp: fresh, ThreadRootSeq: rootMessage.Seq})
			if err != nil {
				t.Fatal(err)
			}
			current, err := s.AddMessage(ctx, "chat", domain.Message{UserID: "alice", Text: "current root", Timestamp: fresh})
			if err != nil {
				t.Fatal(err)
			}
			expiredReply, err := s.AddMessage(ctx, "chat", domain.Message{UserID: "bob", Text: "old reply", Timestamp: old, ThreadRootSeq: current.Seq})
			if err != nil {
				t.Fatal(err)
			}
			poll := &domain.Poll{ID: "poll", ChannelID: "chat", Question: "question", CreatedBy: "alice", CreatedAt: old}
			if err := s.SavePoll(ctx, poll); err != nil {
				t.Fatal(err)
			}
			pollMessage, err := s.AddMessage(ctx, "chat", domain.Message{UserID: "alice", Text: "poll announcement", Timestamp: old, PollRef: &domain.PollReference{ID: "poll"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddPollResponse(ctx, "poll", domain.PollResponse{UserID: "bob"}); err != nil {
				t.Fatal(err)
			}
			schedule := &domain.Schedule{ID: "schedule", ChannelID: "chat", Title: "meeting", CreatedBy: "alice", CreatedAt: old}
			if err := s.SaveSchedule(ctx, schedule); err != nil {
				t.Fatal(err)
			}
			scheduleMessage, err := s.AddMessage(ctx, "chat", domain.Message{UserID: "alice", Text: "schedule announcement", Timestamp: old, ScheduleRef: &domain.ScheduleReference{ID: "schedule", Event: "published"}})
			if err != nil {
				t.Fatal(err)
			}
			laterScheduleMessage, err := s.AddMessage(ctx, "chat", domain.Message{UserID: "alice", Text: "later schedule announcement", Timestamp: fresh, ScheduleRef: &domain.ScheduleReference{ID: "schedule", Event: "finalized"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddScheduleResponse(ctx, "schedule", domain.ScheduleResponse{UserID: "bob"}); err != nil {
				t.Fatal(err)
			}
			if err := s.SetReadState(ctx, "bob", "chat", reply.Seq); err != nil {
				t.Fatal(err)
			}
			if err := s.SetMentionRead(ctx, "bob", rootMessage.ID, fresh); err != nil {
				t.Fatal(err)
			}
			if err := s.SetThreadReadState(ctx, "bob", "chat", rootMessage.Seq, reply.Seq); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SetReaction(ctx, "chat", domain.ReactionEvent{MessageSeq: rootMessage.Seq, UserID: "bob", Key: "ack", Active: true}); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveWithdrawal(ctx, domain.Withdrawal{Kind: "message", ChannelID: "chat", TargetID: "1", ActorID: "alice", At: fresh}); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			preview, err := Prune(ctx, root, kind, cutoff, false)
			if err != nil || preview.Messages != 6 || preview.Polls != 1 || preview.Schedules != 1 {
				t.Fatalf("preview=%+v err=%v", preview, err)
			}
			applied, err := Prune(ctx, root, kind, cutoff, true)
			if err != nil || applied != preview {
				t.Fatalf("applied=%+v err=%v", applied, err)
			}
			s, err = Open(root, kind)
			if err != nil {
				t.Fatal(err)
			}
			for _, seq := range []int64{rootMessage.Seq, reply.Seq, expiredReply.Seq, pollMessage.Seq, scheduleMessage.Seq, laterScheduleMessage.Seq} {
				if _, err := s.GetMessage(ctx, "chat", seq); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("deleted seq %d still exists: %v", seq, err)
				}
			}
			if _, err := s.GetMessage(ctx, "chat", current.Seq); err != nil {
				t.Fatal(err)
			}
			if _, unread, err := s.MessageReadCounts(ctx, "chat", 0); err != nil || unread != 1 {
				t.Fatalf("unread=%d err=%v", unread, err)
			}
			if mentions, err := s.ListMentions(ctx, "bob"); err != nil || len(mentions) != 0 {
				t.Fatalf("mentions=%d err=%v", len(mentions), err)
			}
			if reactions, err := s.GetReactions(ctx, "chat", 0); err != nil || len(reactions) != 0 {
				t.Fatalf("reactions=%d err=%v", len(reactions), err)
			}
			if _, err := s.GetPoll(ctx, "poll"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("poll remains: %v", err)
			}
			if _, err := s.GetSchedule(ctx, "schedule"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("schedule remains: %v", err)
			}
			if _, err := s.GetWithdrawal(ctx, "message", "chat", "1"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("withdrawal remains: %v", err)
			}
			newMessage, err := s.AddMessage(ctx, "chat", domain.Message{UserID: "alice", Text: "after retention"})
			if err != nil || newMessage.Seq <= laterScheduleMessage.Seq {
				t.Fatalf("new seq=%d err=%v", newMessage.Seq, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if kind == "file" {
				if _, err := Migrate(ctx, root, false); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "check.txt"), []byte("ok"), 0600); err != nil {
					t.Fatal(err)
				}
				migrated, err := Open(root, "sqlite")
				if err != nil {
					t.Fatal(err)
				}
				next, err := migrated.AddMessage(ctx, "chat", domain.Message{UserID: "alice", Text: "after migration"})
				if err != nil || next.Seq != newMessage.Seq+1 {
					t.Fatalf("migrated seq=%d err=%v", next.Seq, err)
				}
				if err := migrated.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSQLiteRetentionPendingRecovery(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cutoff := time.Date(2025, 1, 1, 0, 0, 0, 0, time.FixedZone("JST", 9*3600))
	s, err := Open(root, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(ctx, "chat", domain.Message{UserID: "alice", Text: "old", Timestamp: cutoff.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := Prune(ctx, root, "sqlite", cutoff, true)
	if err != nil || report.Messages != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	b, err := json.Marshal(struct {
		Cutoff time.Time `json:"cutoff"`
		Report any       `json:"report"`
	}{cutoff, report})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, sqliteRetentionJournal), b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, "sqlite"); err == nil {
		t.Fatal("normal startup accepted pending retention")
	}
	resumed, err := Prune(ctx, root, "sqlite", cutoff, true)
	if err != nil || !resumed.Cutoff.Equal(report.Cutoff) || resumed.Messages != report.Messages || resumed.Polls != report.Polls || resumed.Schedules != report.Schedules {
		t.Fatalf("resumed=%+v err=%v", resumed, err)
	}
	s, err = Open(root, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionFileAllMessagesAndBackupRestore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	backup := filepath.Join(t.TempDir(), "restored")
	cutoff := time.Date(2025, 1, 1, 0, 0, 0, 0, time.FixedZone("JST", 9*3600))
	s, err := Open(root, "file")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUser(ctx, &domain.User{ID: "alice", Name: "Alice", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChannel(ctx, &domain.Channel{ID: "chat", Name: "Chat", Type: domain.ChannelPublic}); err != nil {
		t.Fatal(err)
	}
	old, err := s.AddMessage(ctx, "chat", domain.Message{UserID: "alice", Text: "old", Timestamp: cutoff.Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(backup, os.DirFS(root)); err != nil {
		t.Fatal(err)
	}
	if _, err := Prune(ctx, root, "file", cutoff, true); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root, "file")
	if err != nil {
		t.Fatal(err)
	}
	seq, err := s.LatestMessageSeq(ctx, "chat")
	if err != nil || seq != old.Seq {
		t.Fatalf("watermark=%d err=%v", seq, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, root, false); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	newMessage, err := s.AddMessage(ctx, "chat", domain.Message{UserID: "alice", Text: "new"})
	if err != nil || newMessage.Seq != old.Seq+1 {
		t.Fatalf("new seq=%d err=%v", newMessage.Seq, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(backup, "file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restored.GetMessage(ctx, "chat", old.Seq); err != nil {
		t.Fatal(err)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
}
