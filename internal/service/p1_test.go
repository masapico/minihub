package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/service"
	"github.com/masapico/minihub/internal/storage/storetest"
	"golang.org/x/crypto/bcrypt"
)

func TestSelfPasswordChangeAndGeneration(t *testing.T) {
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.MinCost)
	if err := store.SaveUser(context.Background(), &domain.User{ID: "alice", Name: "Alice", Role: domain.RoleUser, Enabled: true, PasswordHash: string(hash)}); err != nil {
		t.Fatal(err)
	}
	disabled := service.New(store)
	if _, err := disabled.ChangePassword(context.Background(), "alice", "old-password", "new-password"); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("disabled error=%v", err)
	}
	svc := service.NewWithOptions(store, true, 365*24*time.Hour)
	if _, err := svc.ChangePassword(context.Background(), "alice", "wrong", "new-password"); !errors.Is(err, service.ErrInvalid) {
		t.Fatalf("wrong current error=%v", err)
	}
	generation, err := svc.ChangePassword(context.Background(), "alice", "old-password", "新しい安全なpassword")
	if err != nil || generation != 1 {
		t.Fatalf("generation=%d error=%v", generation, err)
	}
	user, _ := store.GetUser(context.Background(), "alice")
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("新しい安全なpassword")) != nil {
		t.Fatal("new password was not stored")
	}
}

func TestMentionRecipientsAreSnapshottedAndReadIndividually(t *testing.T) {
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.User{{ID: "admin", Name: "Admin", Role: domain.RoleAdmin, Enabled: true}, {ID: "alice", Name: "Alice", Groups: []string{"sales"}, Role: domain.RoleUser, Enabled: true}, {ID: "bob", Name: "Bob", Role: domain.RoleUser, Enabled: true}} {
		u := user
		if err := store.SaveUser(context.Background(), &u); err != nil {
			t.Fatal(err)
		}
	}
	svc := service.NewWithOptions(store, true, 365*24*time.Hour)
	if _, err := svc.CreateChannel(context.Background(), "admin", domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic}); err != nil {
		t.Fatal(err)
	}
	msg, err := svc.PostMessage(context.Background(), "admin", "general", "@group:sales（営業） @bob（Bob） 確認してください")
	if err != nil || len(msg.MentionUserIDs) != 2 {
		t.Fatalf("message=%+v error=%v", msg, err)
	}
	page, err := svc.ListMentions(context.Background(), "alice", "", 50)
	if err != nil || len(page.Mentions) != 1 || page.UnreadCount != 1 {
		t.Fatalf("page=%+v error=%v", page, err)
	}
	if err := svc.MarkMentionRead(context.Background(), "alice", msg.ID); err != nil {
		t.Fatal(err)
	}
	page, _ = svc.ListMentions(context.Background(), "alice", "", 50)
	if page.UnreadCount != 0 || page.Mentions[0].ReadAt == nil {
		t.Fatalf("page=%+v", page)
	}
	user, _ := store.GetUser(context.Background(), "alice")
	user.Groups = nil
	_ = store.SaveUser(context.Background(), user)
	page, _ = svc.ListMentions(context.Background(), "alice", "", 50)
	if len(page.Mentions) != 1 {
		t.Fatal("snapshotted group mention disappeared after membership change")
	}
}

func TestIndividualMentionIncludesGroupDerivedPrivateMember(t *testing.T) {
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.User{
		{ID: "admin", Name: "Admin", Role: domain.RoleAdmin, Enabled: true},
		{ID: "alice", Name: "Alice", Groups: []string{"sales"}, Role: domain.RoleUser, Enabled: true},
		{ID: "outsider", Name: "Outsider", Role: domain.RoleUser, Enabled: true},
	} {
		u := user
		if err := store.SaveUser(context.Background(), &u); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveGroup(context.Background(), &domain.Group{ID: "sales", Name: "Sales"}); err != nil {
		t.Fatal(err)
	}
	svc := service.New(store)
	if _, err := svc.CreateChannel(context.Background(), "admin", domain.Channel{
		ID: "sales-room", Name: "Sales room", Type: domain.ChannelPrivate, Groups: []string{"sales"},
	}); err != nil {
		t.Fatal(err)
	}

	message, err := svc.PostMessage(context.Background(), "admin", "sales-room", "@alice（Alice） @outsider（Outsider） please review")
	if err != nil {
		t.Fatal(err)
	}
	if len(message.MentionUserIDs) != 1 || message.MentionUserIDs[0] != "alice" {
		t.Fatalf("mention recipients=%v, want only group-derived member alice", message.MentionUserIDs)
	}
	aliceMentions, err := svc.ListMentions(context.Background(), "alice", "", 50)
	if err != nil || len(aliceMentions.Mentions) != 1 {
		t.Fatalf("alice mentions=%+v error=%v", aliceMentions, err)
	}
	outsiderMentions, err := svc.ListMentions(context.Background(), "outsider", "", 50)
	if err != nil || len(outsiderMentions.Mentions) != 0 {
		t.Fatalf("outsider mentions=%+v error=%v", outsiderMentions, err)
	}
}
