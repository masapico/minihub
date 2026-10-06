package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/service"
	"github.com/masapico/minihub/internal/storage"
	"github.com/masapico/minihub/internal/storage/storetest"
)

func TestWithdrawalKeepsRawDataAndHidesPublicViews(t *testing.T) {
	ctx := context.Background()
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []domain.User{{ID: "admin", Role: domain.RoleAdmin, Enabled: true}, {ID: "alice", Enabled: true}, {ID: "bob", Enabled: true}, {ID: "manager", Enabled: true}} {
		if err := store.SaveUser(ctx, &u); err != nil {
			t.Fatal(err)
		}
	}
	svc := service.New(store)
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "private", Name: "Private", Type: domain.ChannelPrivate, Members: []string{"alice", "bob", "manager"}, Managers: []string{"manager"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ChangeChannelMembers(ctx, "admin", "private", service.ChannelMemberChanges{RemoveUsers: []string{"admin"}, RemoveManagers: []string{"admin"}}); err != nil {
		t.Fatal(err)
	}
	m, err := svc.PostMessage(ctx, "alice", "private", "secret body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostThreadMessage(ctx, "bob", "private", m.Seq, "old reply"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.WithdrawMessage(ctx, "bob", "private", m.Seq); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("member withdrew another user's message: %v", err)
	}
	first, err := svc.WithdrawMessage(ctx, "manager", "private", m.Seq)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.WithdrawMessage(ctx, "manager", "private", m.Seq)
	if err != nil {
		t.Fatal(err)
	}
	if first.WithdrawnBy != second.WithdrawnBy || !first.WithdrawnAt.Equal(*second.WithdrawnAt) {
		t.Fatal("withdrawal retry changed audit record")
	}
	page, err := svc.GetMessages(ctx, "bob", "private", storage.MessageQuery{Limit: 10})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "" || page.Messages[0].WithdrawnAt == nil {
		t.Fatalf("public message=%+v err=%v", page.Messages, err)
	}
	raw, err := store.GetMessage(ctx, "private", m.Seq)
	if err != nil || raw.Text != "secret body" {
		t.Fatalf("raw message=%+v err=%v", raw, err)
	}
	if _, err := svc.PostThreadMessage(ctx, "bob", "private", m.Seq, "reply"); !errors.Is(err, service.ErrInvalid) {
		t.Fatalf("reply to withdrawn root: %v", err)
	}
	thread, err := svc.GetThreadMessages(ctx, "bob", "private", m.Seq, storage.MessageQuery{Limit: 10})
	if err != nil || thread.Root.Text != "" || len(thread.Messages) != 1 || thread.Messages[0].Text != "old reply" {
		t.Fatalf("existing reply=%+v err=%v", thread, err)
	}
	if _, err := svc.WithdrawMessage(ctx, "admin", "private", m.Seq); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("nonmember administrator accessed private message: %v", err)
	}

	p, err := svc.CreatePoll(ctx, "alice", service.NewPoll{ChannelID: "private", Question: "secret question", Options: []string{"A", "B"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VotePoll(ctx, "bob", p.Poll.ID, 0, []string{"o1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.WithdrawPoll(ctx, "manager", p.Poll.ID); err != nil {
		t.Fatal(err)
	}
	detail, err := svc.GetPoll(ctx, "bob", p.Poll.ID)
	if err != nil || detail.Poll.Question != "" || detail.Respondents != 0 || detail.Poll.Status != "withdrawn" {
		t.Fatalf("public poll=%+v err=%v", detail, err)
	}
	rawPoll, err := store.GetPoll(ctx, p.Poll.ID)
	if err != nil || rawPoll.Question != "secret question" {
		t.Fatalf("raw poll=%+v err=%v", rawPoll, err)
	}
	if _, err := svc.VotePoll(ctx, "bob", p.Poll.ID, 1, []string{"o2"}); !errors.Is(err, service.ErrInvalid) {
		t.Fatalf("vote after withdrawal: %v", err)
	}
	page, err = svc.GetMessages(ctx, "bob", "private", storage.MessageQuery{Limit: 10})
	if err != nil || page.Messages[1].Text != "" || page.Messages[1].PollRef == nil {
		t.Fatalf("poll announcement=%+v err=%v", page.Messages, err)
	}

	schedule, err := svc.CreateSchedule(ctx, "alice", service.NewSchedule{ChannelID: "private", Title: "secret plan", Candidates: []domain.ScheduleCandidate{{Date: "2027-01-10"}, {Date: "2027-01-11"}}})
	if err != nil {
		t.Fatal(err)
	}
	schedule, err = svc.PublishSchedule(ctx, "alice", schedule.ID, schedule.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.WithdrawSchedule(ctx, "manager", schedule.ID); err != nil {
		t.Fatal(err)
	}
	sd, err := svc.GetSchedule(ctx, "bob", schedule.ID)
	if err != nil || sd.Schedule.Title != "" || len(sd.Schedule.Candidates) != 0 || sd.Schedule.Status != domain.ScheduleStatusWithdrawn {
		t.Fatalf("public schedule=%+v err=%v", sd, err)
	}
	rawSchedule, err := store.GetSchedule(ctx, schedule.ID)
	if err != nil || rawSchedule.Title != "secret plan" {
		t.Fatalf("raw schedule=%+v err=%v", rawSchedule, err)
	}
	if _, err := svc.SetScheduleResponse(ctx, "bob", schedule.ID, schedule.Revision, map[string]domain.ScheduleChoice{}, ""); !errors.Is(err, service.ErrInvalid) {
		t.Fatalf("response after withdrawal: %v", err)
	}
	page, err = svc.GetMessages(ctx, "bob", "private", storage.MessageQuery{Limit: 10})
	if err != nil || page.Messages[2].Text != "" || page.Messages[2].ScheduleRef == nil {
		t.Fatalf("schedule announcement=%+v err=%v", page.Messages, err)
	}
	if restored, err := svc.RestoreMessage(ctx, "manager", "private", m.Seq); err != nil || restored.Text != "secret body" {
		t.Fatalf("restored message=%+v err=%v", restored, err)
	}
	if _, err := svc.RestoreMessage(ctx, "manager", "private", m.Seq); err != nil {
		t.Fatal(err)
	}
	if restored, err := svc.RestorePoll(ctx, "manager", p.Poll.ID); err != nil || restored.Poll.Question != "secret question" || restored.Respondents != 1 {
		t.Fatalf("restored poll=%+v err=%v", restored, err)
	}
	if restored, err := svc.RestoreSchedule(ctx, "manager", schedule.ID); err != nil || restored.Schedule.Title != "secret plan" {
		t.Fatalf("restored schedule=%+v err=%v", restored, err)
	}
	if _, err := svc.WithdrawMessage(ctx, "alice", "private", m.Seq); err != nil {
		t.Fatal(err)
	}
	w, err := store.GetWithdrawal(ctx, "message", "private", "1")
	if err != nil || len(w.Events) != 3 || w.Events[0].Action != "withdraw" || w.Events[1].Action != "restore" || w.Events[2].Action != "withdraw" {
		t.Fatalf("withdrawal history=%+v err=%v", w, err)
	}
	if _, err := svc.RestoreMessage(ctx, "bob", "private", m.Seq); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("unauthorized restore: %v", err)
	}
}
