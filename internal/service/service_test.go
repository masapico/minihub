package service_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/service"
	"github.com/masapico/minihub/internal/storage"
	"github.com/masapico/minihub/internal/storage/storetest"
)

type recordingPublisher struct {
	channelID      string
	seq            int64
	reactionSeq    int64
	channelChanges int
}

type failingReadStorage struct{ storage.Storage }

func (s failingReadStorage) SetReadState(context.Context, string, string, int64) error {
	return errors.New("read state unavailable")
}

func (p *recordingPublisher) ReactionChanged(_ context.Context, channelID string, seq int64) {
	p.channelID, p.reactionSeq = channelID, seq
}

func (p *recordingPublisher) NewMessage(_ context.Context, channelID string, seq int64) {
	p.channelID, p.seq = channelID, seq
}

func (p *recordingPublisher) ChannelsChanged(context.Context) { p.channelChanges++ }

func TestDelegatedChannelManagerTransfer(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "secret", Name: "Secret", Type: domain.ChannelPrivate}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ChangeChannelMembers(ctx, "admin", "secret", service.ChannelMemberChanges{AddManagers: []string{"alice"}, RemoveManagers: []string{"admin"}}); err != nil {
		t.Fatal(err)
	}
	detail, err := svc.GetChannelManagement(ctx, "alice", "secret")
	if err != nil || !detail.Capabilities.ManageMembers || !memberID(detail.Members, "alice") {
		t.Fatalf("delegated detail=%#v err=%v", detail, err)
	}
	if _, err := svc.ChangeChannelMembers(ctx, "alice", "secret", service.ChannelMemberChanges{AddManagers: []string{"bob"}, RemoveManagers: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetChannelManagement(ctx, "alice", "secret"); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("former manager error=%v", err)
	}
	if _, err := svc.ChangeChannelMembers(ctx, "bob", "secret", service.ChannelMemberChanges{RemoveManagers: []string{"bob"}}); !errors.Is(err, service.ErrInvalid) {
		t.Fatalf("last manager removal error=%v", err)
	}
	if _, err := svc.LeaveChannel(ctx, "bob", "secret"); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("private leave error=%v", err)
	}
}

func TestChannelArchiveRestorePreservesHistory(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic, Members: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	message, err := svc.PostMessage(ctx, "alice", "general", "kept")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ArchiveChannel(ctx, "admin", "general"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetMessages(ctx, "alice", "general", storage.MessageQuery{Limit: 10}); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("archived read error=%v", err)
	}
	channels, err := svc.ListChannels(ctx, "alice")
	if err != nil || len(channels) != 0 {
		t.Fatalf("visible archived channels=%#v err=%v", channels, err)
	}
	managed, err := svc.ListManageableChannels(ctx, "admin", true)
	if err != nil || len(managed) != 1 || managed[0].ArchivedAt == nil {
		t.Fatalf("managed=%#v err=%v", managed, err)
	}
	if err := svc.RestoreChannel(ctx, "admin", "general"); err != nil {
		t.Fatal(err)
	}
	page, err := svc.GetMessages(ctx, "alice", "general", storage.MessageQuery{Limit: 10})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Seq != message.Seq {
		t.Fatalf("restored page=%#v err=%v", page, err)
	}
}

func TestConcurrentChannelMemberChangesAreMerged(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, id := range []string{"alice", "bob"} {
		id := id
		go func() {
			<-start
			_, err := svc.ChangeChannelMembers(ctx, "admin", "general", service.ChannelMemberChanges{AddUsers: []string{id}})
			errs <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	detail, err := svc.GetChannelManagement(ctx, "admin", "general")
	if err != nil {
		t.Fatal(err)
	}
	if !memberID(detail.Members, "alice") || !memberID(detail.Members, "bob") {
		t.Fatalf("members=%v", detail.Members)
	}
}

func memberID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func setup(t *testing.T) *service.Service {
	t.Helper()
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.User{
		{ID: "admin", Name: "Admin", Role: domain.RoleAdmin, Enabled: true},
		{ID: "alice", Name: "Alice", Role: domain.RoleUser, Enabled: true},
		{ID: "bob", Name: "Bob", Role: domain.RoleUser, Enabled: true},
		{ID: "disabled", Name: "Disabled", Role: domain.RoleUser, Enabled: false},
	} {
		user := user
		if err := store.SaveUser(context.Background(), &user); err != nil {
			t.Fatal(err)
		}
	}
	return service.New(store)
}

func TestMentionCandidateIDsRespectMembership(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "secret", Name: "Secret", Type: domain.ChannelPrivate, Members: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	ids, err := svc.MentionCandidateIDs(ctx, "alice", "secret")
	if err != nil || len(ids) != 1 || ids[0] != "admin" {
		t.Fatalf("candidate IDs=%v error=%v", ids, err)
	}
	if _, err := svc.MentionCandidateIDs(ctx, "bob", "secret"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("nonmember access=%v", err)
	}
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic, Members: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	before, err := svc.MentionCandidateIDs(ctx, "alice", "general")
	if err != nil || len(before) != 1 || before[0] != "admin" {
		t.Fatalf("before join=%v %v", before, err)
	}
	if _, err := svc.JoinChannel(ctx, "bob", "general"); err != nil {
		t.Fatal(err)
	}
	after, err := svc.MentionCandidateIDs(ctx, "alice", "general")
	sort.Strings(after)
	if err != nil || len(after) != 2 || after[0] != "admin" || after[1] != "bob" {
		t.Fatalf("after join=%v %v", after, err)
	}
}

func TestMentionCandidateIDsRefreshAfterGroupChange(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	if _, err := svc.CreateGroup(ctx, "admin", domain.Group{ID: "sales", Name: "Sales"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "team", Name: "Team", Type: domain.ChannelPrivate, Groups: []string{"sales"}}); err != nil {
		t.Fatal(err)
	}
	ids, err := svc.MentionCandidateIDs(ctx, "admin", "team")
	if err != nil || len(ids) != 0 {
		t.Fatalf("initial=%v %v", ids, err)
	}
	if _, err := svc.ChangeGroupMembers(ctx, "admin", "sales", []string{"alice"}, nil); err != nil {
		t.Fatal(err)
	}
	ids, err = svc.MentionCandidateIDs(ctx, "admin", "team")
	if err != nil || len(ids) != 1 || ids[0] != "alice" {
		t.Fatalf("after add=%v %v", ids, err)
	}
	if _, err := svc.ChangeGroupMembers(ctx, "admin", "sales", nil, []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	ids, err = svc.MentionCandidateIDs(ctx, "admin", "team")
	if err != nil || len(ids) != 0 {
		t.Fatalf("after removal=%v %v", ids, err)
	}
}

func TestPublicJoinLeaveAndPosting(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	channel, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic})
	if err != nil {
		t.Fatal(err)
	}
	if len(channel.Members) != 1 || channel.Members[0] != "admin" {
		t.Fatalf("creator was not joined: %#v", channel.Members)
	}
	if _, err := svc.GetMessages(ctx, "bob", "general", storage.MessageQuery{Limit: 100}); err != nil {
		t.Fatalf("public read should be allowed: %v", err)
	}
	if _, err := svc.PostMessage(ctx, "bob", "general", "before join"); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("post before join error=%v", err)
	}
	if _, err := svc.JoinChannel(ctx, "bob", "general"); err != nil {
		t.Fatal(err)
	}
	msg, err := svc.PostMessage(ctx, "bob", "general", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkRead(ctx, "bob", "general", msg.Seq); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LeaveChannel(ctx, "bob", "general"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostMessage(ctx, "bob", "general", "after leave"); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("post after leave error=%v", err)
	}
}

func TestReactionAuthorizationValidationAndUnread(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic, Members: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	message, err := svc.PostMessage(ctx, "alice", "general", "hello")
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.GetReadStatus(ctx, "alice", "general")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetReaction(ctx, "bob", "general", message.Seq, "ack", true); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("non-member reaction error=%v", err)
	}
	if _, err := svc.SetReaction(ctx, "alice", "general", message.Seq, "party", true); !errors.Is(err, service.ErrInvalid) {
		t.Fatalf("unsupported reaction error=%v", err)
	}
	publisher := new(recordingPublisher)
	svc.SetMessagePublisher(publisher)
	view, err := svc.SetReaction(ctx, "alice", "general", message.Seq, "ack", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Reactions) != 1 || view.Reactions[0].Count != 1 || !view.Reactions[0].ReactedByMe {
		t.Fatalf("unexpected reaction view: %#v", view)
	}
	if publisher.reactionSeq != message.Seq {
		t.Fatalf("reaction event seq=%d", publisher.reactionSeq)
	}
	userIDs, err := svc.GetReactionUsers(ctx, "bob", "general", message.Seq, "ack")
	if err != nil || len(userIDs) != 1 || userIDs[0] != "alice" {
		t.Fatalf("reaction users=%#v err=%v", userIDs, err)
	}
	if _, err := svc.GetReactionUsers(ctx, "bob", "general", message.Seq, "party"); !errors.Is(err, service.ErrInvalid) {
		t.Fatalf("unsupported reaction users key error=%v", err)
	}
	after, err := svc.GetReadStatus(ctx, "alice", "general")
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("reaction changed unread status: before=%#v after=%#v", before, after)
	}
}

func TestPrivateChannelDoesNotLeakToNonMember(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "secret", Name: "Secret", Type: domain.ChannelPrivate, Members: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	channels, err := svc.ListChannels(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 0 {
		t.Fatalf("private channel leaked in list: %#v", channels)
	}
	if _, err := svc.GetChannel(ctx, "bob", "secret"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetChannel error=%v", err)
	}
	if _, err := svc.GetMessages(ctx, "bob", "secret", storage.MessageQuery{Limit: 100}); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetMessages error=%v", err)
	}
	if _, err := svc.GetReactionUsers(ctx, "bob", "secret", 1, "ack"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetReactionUsers error=%v", err)
	}
	if _, err := svc.PostMessage(ctx, "bob", "secret", "nope"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("PostMessage error=%v", err)
	}
	if _, err := svc.JoinChannel(ctx, "bob", "secret"); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("JoinChannel error=%v", err)
	}
	if _, err := svc.PostMessage(ctx, "alice", "secret", "allowed"); err != nil {
		t.Fatal(err)
	}
}

func TestGroupGrantsDynamicPrivateChannelMembership(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	group, err := svc.CreateGroup(ctx, "admin", domain.Group{ID: "sales", Name: "営業部"})
	if err != nil {
		t.Fatal(err)
	}
	if group.Name != "営業部" {
		t.Fatalf("group=%#v", group)
	}
	if _, err := svc.ChangeGroupMembers(ctx, "admin", "sales", []string{"alice"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "sales-room", Name: "営業部", Type: domain.ChannelPrivate, Groups: []string{"sales"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetMessages(ctx, "alice", "sales-room", storage.MessageQuery{Limit: 100}); err != nil {
		t.Fatalf("group member cannot read: %v", err)
	}
	if _, err := svc.PostMessage(ctx, "alice", "sales-room", "参加しました"); err != nil {
		t.Fatalf("group member cannot post: %v", err)
	}
	if _, err := svc.GetMessages(ctx, "bob", "sales-room", storage.MessageQuery{Limit: 100}); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("non-member read error=%v", err)
	}
	if _, err := svc.ChangeGroupMembers(ctx, "admin", "sales", nil, []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetMessages(ctx, "alice", "sales-room", storage.MessageQuery{Limit: 100}); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("removed group membership remained effective: %v", err)
	}
}

func TestGroupDetailMembershipChangesAndCascadingDelete(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	if _, err := svc.CreateGroup(ctx, "admin", domain.Group{ID: "sales", Name: "営業部"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "sales-room", Name: "営業部", Type: domain.ChannelPrivate, Members: []string{"bob"}, Groups: []string{"sales"}}); err != nil {
		t.Fatal(err)
	}
	detail, err := svc.ChangeGroupMembers(ctx, "admin", "sales", []string{"alice", "disabled", "alice"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Members) != 2 || len(detail.Channels) != 1 {
		t.Fatalf("detail=%#v", detail)
	}
	if _, err := svc.ChangeGroupMembers(ctx, "admin", "sales", []string{"alice"}, []string{"alice"}); !errors.Is(err, service.ErrInvalid) {
		t.Fatalf("overlapping membership change error=%v", err)
	}
	if _, err := svc.UpdateUser(ctx, "admin", "alice", service.UpdateUser{Name: "Alice", Role: domain.RoleUser, Enabled: true, Groups: []string{"sales"}, GroupsProvided: true}); err != nil {
		t.Fatalf("unchanged legacy groups rejected: %v", err)
	}
	if _, err := svc.UpdateUser(ctx, "admin", "alice", service.UpdateUser{Name: "Alice", Role: domain.RoleUser, Enabled: true, Groups: []string{"other"}, GroupsProvided: true}); !errors.Is(err, service.ErrInvalid) {
		t.Fatalf("changed groups error=%v", err)
	}
	if err := svc.DeleteGroup(ctx, "admin", "sales"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetGroupDetail(ctx, "admin", "sales"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("deleted group detail error=%v", err)
	}
	users, err := svc.ListUsers(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if memberForTest(user.Groups, "sales") {
			t.Fatalf("group reference remained on user %s", user.ID)
		}
	}
	channel, err := svc.GetChannel(ctx, "admin", "sales-room")
	if err != nil {
		t.Fatal(err)
	}
	if memberForTest(channel.Groups, "sales") {
		t.Fatal("group reference remained on channel")
	}
	if _, err := svc.GetMessages(ctx, "alice", "sales-room", storage.MessageQuery{Limit: 10}); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("removed group member retained access: %v", err)
	}
	if _, err := svc.GetMessages(ctx, "bob", "sales-room", storage.MessageQuery{Limit: 10}); err != nil {
		t.Fatalf("direct member lost access: %v", err)
	}
}

func memberForTest(ids []string, wanted string) bool {
	for _, id := range ids {
		if id == wanted {
			return true
		}
	}
	return false
}

func TestGroupAdministrationRequiresAdmin(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	if _, err := svc.CreateGroup(ctx, "admin", domain.Group{ID: "sales", Name: "Sales"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetGroupDetail(ctx, "alice", "sales"); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("detail error=%v", err)
	}
	if _, err := svc.ChangeGroupMembers(ctx, "alice", "sales", []string{"alice"}, nil); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("membership error=%v", err)
	}
	if err := svc.DeleteGroup(ctx, "alice", "sales"); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("delete error=%v", err)
	}
}

func TestConcurrentGroupMembershipChangesAreSerialized(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	if _, err := svc.CreateGroup(ctx, "admin", domain.Group{ID: "sales", Name: "Sales"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		userID := "alice"
		if i%2 == 1 {
			userID = "bob"
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.ChangeGroupMembers(ctx, "admin", "sales", []string{userID}, nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	detail, err := svc.GetGroupDetail(ctx, "admin", "sales")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Members) != 2 {
		t.Fatalf("members=%#v", detail.Members)
	}
}

func TestAdminAndEnabledUserAuthorization(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	_, err := svc.CreateChannel(ctx, "alice", domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic})
	if !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("non-admin error=%v", err)
	}
	if _, err := svc.ListChannels(ctx, "disabled"); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("disabled user error=%v", err)
	}
}

func TestReadMarkerCannotPassLatestMessage(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic, Members: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostMessage(ctx, "alice", "general", "one"); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkRead(ctx, "alice", "general", 2); !errors.Is(err, service.ErrInvalid) {
		t.Fatalf("error=%v", err)
	}
}

func TestReadStatusTracksUnreadBoundary(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "status", Name: "Status", Type: domain.ChannelPublic, Members: []string{"alice", "bob"}}); err != nil {
		t.Fatal(err)
	}
	first, _ := svc.PostMessage(ctx, "bob", "status", "one")
	_, _ = svc.PostMessage(ctx, "bob", "status", "two")
	if err := svc.MarkRead(ctx, "alice", "status", first.Seq); err != nil {
		t.Fatal(err)
	}
	status, err := svc.GetReadStatus(ctx, "alice", "status")
	if err != nil {
		t.Fatal(err)
	}
	if status.LastReadSeq != 1 || status.LatestSeq != 2 || status.UnreadCount != 1 {
		t.Fatalf("status=%+v", status)
	}
}

func TestReadStatusesReturnAllVisibleChannels(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	for _, channel := range []domain.Channel{
		{ID: "public", Name: "Public", Type: domain.ChannelPublic, Members: []string{"bob"}},
		{ID: "private", Name: "Private", Type: domain.ChannelPrivate, Members: []string{"alice", "bob"}},
		{ID: "secret", Name: "Secret", Type: domain.ChannelPrivate, Members: []string{"bob"}},
		{ID: "archived", Name: "Archived", Type: domain.ChannelPublic, Members: []string{"bob"}},
	} {
		if _, err := svc.CreateChannel(ctx, "admin", channel); err != nil {
			t.Fatal(err)
		}
	}
	for _, channelID := range []string{"public", "private"} {
		if _, err := svc.PostMessage(ctx, "bob", channelID, "one"); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.PostMessage(ctx, "bob", channelID, "two"); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.MarkRead(ctx, "alice", "private", 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.ArchiveChannel(ctx, "admin", "archived"); err != nil {
		t.Fatal(err)
	}
	statuses, err := svc.GetReadStatuses(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 {
		t.Fatalf("statuses=%v", statuses)
	}
	if got := statuses["public"]; got.LastReadSeq != 0 || got.LatestSeq != 2 || got.UnreadCount != 0 || got.ThreadUpdates {
		t.Fatalf("public status=%+v", got)
	}
	if got := statuses["private"]; got.LastReadSeq != 1 || got.LatestSeq != 2 || got.UnreadCount != 1 {
		t.Fatalf("private status=%+v", got)
	}
	if _, exists := statuses["secret"]; exists {
		t.Fatal("unauthorized private channel was returned")
	}
	if _, exists := statuses["archived"]; exists {
		t.Fatal("archived channel was returned")
	}
}

func TestPublicJoinStartsUnreadAtCurrentMessage(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "public", Name: "Public", Type: domain.ChannelPublic, Members: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	first, err := svc.PostMessage(ctx, "alice", "public", "before join")
	if err != nil {
		t.Fatal(err)
	}
	status, err := svc.GetReadStatus(ctx, "bob", "public")
	if err != nil || status.UnreadCount != 0 || status.LatestSeq != first.Seq {
		t.Fatalf("unjoined status=%+v err=%v", status, err)
	}
	if _, err := svc.JoinChannel(ctx, "bob", "public"); err != nil {
		t.Fatal(err)
	}
	status, err = svc.GetReadStatus(ctx, "bob", "public")
	if err != nil || status.LastReadSeq != first.Seq || status.UnreadCount != 0 {
		t.Fatalf("joined status=%+v err=%v", status, err)
	}
	second, err := svc.PostMessage(ctx, "alice", "public", "after join")
	if err != nil {
		t.Fatal(err)
	}
	status, err = svc.GetReadStatus(ctx, "bob", "public")
	if err != nil || status.UnreadCount != 1 || status.LatestSeq != second.Seq {
		t.Fatalf("new message status=%+v err=%v", status, err)
	}
	if _, err := svc.LeaveChannel(ctx, "bob", "public"); err != nil {
		t.Fatal(err)
	}
	status, err = svc.GetReadStatus(ctx, "bob", "public")
	if err != nil || status.UnreadCount != 0 {
		t.Fatalf("left status=%+v err=%v", status, err)
	}
	if _, err := svc.JoinChannel(ctx, "bob", "public"); err != nil {
		t.Fatal(err)
	}
	status, err = svc.GetReadStatus(ctx, "bob", "public")
	if err != nil || status.LastReadSeq != second.Seq || status.UnreadCount != 0 {
		t.Fatalf("rejoined status=%+v err=%v", status, err)
	}
}

func TestPostingAdvancesAuthorReadState(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "status", Name: "Status", Type: domain.ChannelPublic, Members: []string{"alice", "bob"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostMessage(ctx, "bob", "status", "unread for Alice"); err != nil {
		t.Fatal(err)
	}
	posted, err := svc.PostMessage(ctx, "alice", "status", "Alice replies")
	if err != nil {
		t.Fatal(err)
	}
	authorStatus, err := svc.GetReadStatus(ctx, "alice", "status")
	if err != nil {
		t.Fatal(err)
	}
	if authorStatus.LastReadSeq != posted.Seq || authorStatus.UnreadCount != 0 {
		t.Fatalf("author status=%+v posted=%+v", authorStatus, posted)
	}
	otherStatus, err := svc.GetReadStatus(ctx, "bob", "status")
	if err != nil {
		t.Fatal(err)
	}
	if otherStatus.UnreadCount != 1 {
		t.Fatalf("other poster status=%+v, want Alice's reply unread", otherStatus)
	}
}

func TestPostPublishesCommittedMessageWhenAuthorReadUpdateFails(t *testing.T) {
	ctx := context.Background()
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.User{
		{ID: "admin", Name: "Admin", Role: domain.RoleAdmin, Enabled: true},
		{ID: "alice", Name: "Alice", Role: domain.RoleUser, Enabled: true},
	} {
		user := user
		if err := store.SaveUser(ctx, &user); err != nil {
			t.Fatal(err)
		}
	}
	base := service.New(store)
	if _, err := base.CreateChannel(ctx, "admin", domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic, Members: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	svc := service.New(failingReadStorage{Storage: store})
	publisher := new(recordingPublisher)
	svc.SetMessagePublisher(publisher)
	message, err := svc.PostMessage(ctx, "alice", "general", "hello")
	var committed *service.CommittedMessageError
	if !errors.As(err, &committed) || message.Seq != 1 {
		t.Fatalf("message=%+v error=%v", message, err)
	}
	if publisher.channelID != "general" || publisher.seq != 1 {
		t.Fatalf("publisher=%+v", publisher)
	}
	after := int64(0)
	page, err := store.GetMessages(ctx, "general", storage.MessageQuery{AfterSeq: &after, Limit: 10})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("page=%+v error=%v", page, err)
	}
}

func TestCommittedMessageIsPublished(t *testing.T) {
	svc := setup(t)
	publisher := new(recordingPublisher)
	svc.SetMessagePublisher(publisher)
	ctx := context.Background()
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic}); err != nil {
		t.Fatal(err)
	}
	message, err := svc.PostMessage(ctx, "admin", "general", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if publisher.channelID != "general" || publisher.seq != message.Seq {
		t.Fatalf("published channel=%q seq=%d", publisher.channelID, publisher.seq)
	}
}

func TestAdminCanCreateAndListUsers(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	if _, err := svc.CreateGroup(ctx, "admin", domain.Group{ID: "sales", Name: "Sales"}); err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreateUsers(ctx, "admin", []service.NewUser{{ID: "new-user", Name: "新規 利用者", Password: "temporary-pass", Groups: []string{"sales"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0].PasswordHash == "" || created[0].Role != domain.RoleUser {
		t.Fatalf("created=%#v", created)
	}
	users, err := svc.ListUsers(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 5 {
		t.Fatalf("users=%d", len(users))
	}
	if _, err := svc.CreateUsers(ctx, "alice", []service.NewUser{{ID: "denied", Name: "Denied", Password: "temporary-pass"}}); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("non-admin error=%v", err)
	}
	if _, err := svc.CreateUsers(ctx, "admin", []service.NewUser{{ID: "new-user", Name: "Again", Password: "temporary-pass"}}); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("duplicate error=%v", err)
	}
}

func TestAdminCanUpdateUser(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	for _, group := range []domain.Group{{ID: "sales", Name: "Sales"}, {ID: "tokyo", Name: "Tokyo"}} {
		if _, err := svc.CreateGroup(ctx, "admin", group); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.ChangeGroupMembers(ctx, "admin", "sales", []string{"alice"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ChangeGroupMembers(ctx, "admin", "tokyo", []string{"alice"}, nil); err != nil {
		t.Fatal(err)
	}
	updated, err := svc.UpdateUser(ctx, "admin", "alice", service.UpdateUser{
		Name: "Alice Updated", Password: "new-password", Role: domain.RoleAdmin, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Alice Updated" || updated.Role != domain.RoleAdmin || len(updated.Groups) != 2 || updated.PasswordHash == "" {
		t.Fatalf("updated=%#v", updated)
	}
	if _, err := svc.UpdateUser(ctx, "bob", "alice", service.UpdateUser{Name: "Denied", Role: domain.RoleUser, Enabled: true}); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("non-admin error=%v", err)
	}
}

func TestLastEnabledAdminCannotBeDisabledOrDemoted(t *testing.T) {
	svc := setup(t)
	for _, input := range []service.UpdateUser{
		{Name: "Admin", Role: domain.RoleAdmin, Enabled: false},
		{Name: "Admin", Role: domain.RoleUser, Enabled: true},
	} {
		if _, err := svc.UpdateUser(context.Background(), "admin", "admin", input); !errors.Is(err, service.ErrInvalid) {
			t.Fatalf("error=%v, want invalid", err)
		}
	}
}

func TestUserDirectoryOmitsDisabledUsers(t *testing.T) {
	users, err := setup(t).ListUserDirectory(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if user.ID == "disabled" {
			t.Fatal("disabled user exposed in directory")
		}
	}
}
