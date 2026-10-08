package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
)

var (
	ErrUnauthenticated = errors.New("authentication required")
	ErrForbidden       = errors.New("forbidden")
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("conflict")
	ErrStale           = errors.New("stale revision")
	ErrInvalid         = errors.New("invalid request")
)

const MaxMessageBytes = 16 * 1024
const MinPasswordRunes = 8

type NewUser struct {
	ID, Name, Password string
	Groups             []string
	Role               domain.Role
}

type UpdateUser struct {
	Name, Password string
	Groups         []string
	GroupsProvided bool
	Role           domain.Role
	Enabled        bool
}

type GroupMember struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Role    domain.Role `json:"role"`
	Enabled bool        `json:"enabled"`
}

type GroupChannel struct {
	ID   string             `json:"id"`
	Name string             `json:"name"`
	Type domain.ChannelType `json:"type"`
}

type GroupDetail struct {
	*domain.Group
	Members  []GroupMember  `json:"members"`
	Channels []GroupChannel `json:"channels"`
}

type ChannelCapabilities struct {
	ManageMembers  bool `json:"manageMembers"`
	ManageSettings bool `json:"manageSettings"`
	Archive        bool `json:"archive"`
	Restore        bool `json:"restore"`
}

type ChannelDetail struct {
	*domain.Channel
	EffectiveMembers []ChannelMember     `json:"effectiveMembers"`
	Users            []ChannelUser       `json:"users"`
	Capabilities     ChannelCapabilities `json:"capabilities"`
}

type ChannelUser struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type ChannelMember struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Direct    bool     `json:"direct,omitempty"`
	Manager   bool     `json:"manager,omitempty"`
	ViaGroups []string `json:"viaGroups,omitempty"`
}

type ChannelMemberChanges struct {
	AddUsers, RemoveUsers       []string
	AddGroups, RemoveGroups     []string
	AddManagers, RemoveManagers []string
}

type ChannelPublisher interface{ ChannelsChanged(context.Context) }

type ReadStatus struct {
	ThreadUpdates bool  `json:"threadUpdates,omitempty"`
	LastReadSeq   int64 `json:"lastReadSeq"`
	LatestSeq     int64 `json:"latestSeq"`
	UnreadCount   int   `json:"unreadCount"`
}

type ReactionSummary struct {
	Key         string `json:"key"`
	Count       int    `json:"count"`
	ReactedByMe bool   `json:"reactedByMe"`
}

type MessageReactions struct {
	MessageSeq int64             `json:"messageSeq"`
	Reactions  []ReactionSummary `json:"reactions"`
}

type Service struct {
	ai                 *aiDispatcher
	storage            storage.Storage
	attachmentMgr      *AttachmentManager
	pollMu             sync.Mutex
	mu                 sync.Mutex
	userMu             sync.Mutex
	groupMu            sync.RWMutex
	presenceMu         sync.Mutex
	presenceMembers    map[string][]string
	locks              map[string]*sync.Mutex
	publisher          MessagePublisher
	selfPasswordChange bool
	mentionRetention   time.Duration
}

var validUserID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

type MessagePublisher interface {
	NewMessage(context.Context, string, int64)
}

type ReactionPublisher interface {
	ReactionChanged(context.Context, string, int64)
}
type SessionPublisher interface{ DisconnectUser(string) }

func (s *Service) PasswordChanged(userID string) {
	if publisher, ok := s.publisher.(SessionPublisher); ok {
		publisher.DisconnectUser(userID)
	}
}

var reactionKeys = map[string]bool{
	"ack": true, "done": true, "eyes": true, "thanks": true,
}

func New(store storage.Storage) *Service {
	return NewWithOptions(store, false, 365*24*time.Hour)
}

func NewWithOptions(store storage.Storage, selfPasswordChange bool, mentionRetention time.Duration) *Service {
	return &Service{
		storage:            store,
		attachmentMgr:      newAttachmentManager(""),
		locks:              make(map[string]*sync.Mutex),
		presenceMembers:    make(map[string][]string),
		selfPasswordChange: selfPasswordChange,
		mentionRetention:   mentionRetention,
	}
}

func (s *Service) SelfPasswordChangeEnabled() bool { return s.selfPasswordChange }

func (s *Service) SetMessagePublisher(publisher MessagePublisher) {
	s.publisher = publisher
}

func (s *Service) user(ctx context.Context, id string) (*domain.User, error) {
	if id == "" {
		return nil, ErrUnauthenticated
	}
	user, err := s.storage.GetUser(ctx, id)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	if !user.Enabled {
		return nil, ErrForbidden
	}
	return user, nil
}

func (s *Service) Me(ctx context.Context, userID string) (*domain.User, error) {
	return s.user(ctx, userID)
}

func (s *Service) ListUsers(ctx context.Context, actorID string) ([]domain.User, error) {
	actor, err := s.user(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if actor.Role != domain.RoleAdmin {
		return nil, ErrForbidden
	}
	return s.storage.ListUsers(ctx)
}

func (s *Service) ListUserDirectory(ctx context.Context, userID string) ([]domain.User, error) {
	if _, err := s.user(ctx, userID); err != nil {
		return nil, err
	}
	users, err := s.storage.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	enabled := users[:0]
	for _, user := range users {
		if user.Enabled {
			enabled = append(enabled, user)
		}
	}
	return enabled, nil
}

// MentionCandidateIDs returns the users visible in the existing channel mention picker.
func (s *Service) MentionCandidateIDs(ctx context.Context, userID, channelID string) ([]string, error) {
	if err := s.canPost(ctx, userID, channelID); err != nil {
		return nil, err
	}
	s.presenceMu.Lock()
	members, ok := s.presenceMembers[channelID]
	if !ok {
		channel, err := s.channel(ctx, channelID)
		if err != nil {
			s.presenceMu.Unlock()
			return nil, err
		}
		users, err := s.storage.ListUsers(ctx)
		if err != nil {
			s.presenceMu.Unlock()
			return nil, err
		}
		members = make([]string, 0, len(users))
		for i := range users {
			if users[i].Enabled && channelMember(&users[i], channel) {
				members = append(members, users[i].ID)
			}
		}
		s.presenceMembers[channelID] = members
	}
	s.presenceMu.Unlock()
	ids := make([]string, 0, len(members))
	for _, id := range members {
		if id != userID {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (s *Service) ListGroups(ctx context.Context, userID string) ([]domain.Group, error) {
	if _, err := s.user(ctx, userID); err != nil {
		return nil, err
	}
	return s.storage.ListGroups(ctx)
}

func (s *Service) CreateGroup(ctx context.Context, actorID string, group domain.Group) (*domain.Group, error) {
	actor, err := s.user(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if actor.Role != domain.RoleAdmin {
		return nil, ErrForbidden
	}
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	group.ID, group.Name = strings.TrimSpace(group.ID), strings.TrimSpace(group.Name)
	if !validUserID.MatchString(group.ID) || group.Name == "" || utf8.RuneCountInString(group.Name) > 100 {
		return nil, fmt.Errorf("%w: グループには半角英数字のIDと100文字以内の名前が必要です", ErrInvalid)
	}
	if _, err := s.storage.GetGroup(ctx, group.ID); err == nil {
		return nil, ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	group.Version = domain.Version
	if err := s.storage.SaveGroup(ctx, &group); err != nil {
		return nil, err
	}
	return &group, nil
}

func (s *Service) UpdateGroup(ctx context.Context, actorID, groupID, name string) (*domain.Group, error) {
	actor, err := s.user(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if actor.Role != domain.RoleAdmin {
		return nil, ErrForbidden
	}
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return nil, fmt.Errorf("%w: グループ名は必須で、100文字以内にしてください", ErrInvalid)
	}
	group, err := s.storage.GetGroup(ctx, groupID)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	group.Name = name
	if err := s.storage.SaveGroup(ctx, group); err != nil {
		return nil, err
	}
	return group, nil
}

func (s *Service) GetGroupDetail(ctx context.Context, actorID, groupID string) (*GroupDetail, error) {
	actor, err := s.user(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if actor.Role != domain.RoleAdmin {
		return nil, ErrForbidden
	}
	s.groupMu.RLock()
	defer s.groupMu.RUnlock()
	group, err := s.storage.GetGroup(ctx, groupID)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	users, err := s.storage.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	channels, err := s.storage.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	detail := &GroupDetail{Group: group, Members: []GroupMember{}, Channels: []GroupChannel{}}
	for _, user := range users {
		if member(user.Groups, groupID) {
			detail.Members = append(detail.Members, GroupMember{ID: user.ID, Name: user.Name, Role: user.Role, Enabled: user.Enabled})
		}
	}
	for _, channel := range channels {
		if member(channel.Groups, groupID) {
			detail.Channels = append(detail.Channels, GroupChannel{ID: channel.ID, Name: channel.Name, Type: channel.Type})
		}
	}
	return detail, nil
}

func (s *Service) ChangeGroupMembers(ctx context.Context, actorID, groupID string, add, removeIDs []string) (*GroupDetail, error) {
	actor, err := s.user(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if actor.Role != domain.RoleAdmin {
		return nil, ErrForbidden
	}
	add, removeIDs = unique(add), unique(removeIDs)
	for _, id := range add {
		if member(removeIDs, id) {
			return nil, fmt.Errorf("%w: ユーザー %q を同時に追加・解除することはできません", ErrInvalid, id)
		}
	}
	s.groupMu.RLock()
	defer s.groupMu.RUnlock()
	if _, err := s.storage.GetGroup(ctx, groupID); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	changes := make([]*domain.User, 0, len(add)+len(removeIDs))
	seen := make(map[string]bool, len(add)+len(removeIDs))
	for _, id := range append(append([]string{}, add...), removeIDs...) {
		if seen[id] {
			continue
		}
		seen[id] = true
		user, err := s.storage.GetUser(ctx, id)
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: ユーザー %q は存在しないか、無効です", ErrInvalid, id)
		}
		if err != nil {
			return nil, err
		}
		if member(add, id) {
			user.Groups = unique(append(user.Groups, groupID))
		} else {
			user.Groups = remove(user.Groups, groupID)
		}
		changes = append(changes, user)
	}
	for _, user := range changes {
		if err := s.storage.SaveUser(ctx, user); err != nil {
			return nil, err
		}
	}
	detail, err := s.groupDetailLocked(ctx, groupID)
	if err == nil {
		s.publishChannelsChanged(ctx)
	}
	return detail, err
}

func (s *Service) DeleteGroup(ctx context.Context, actorID, groupID string) error {
	actor, err := s.user(ctx, actorID)
	if err != nil {
		return err
	}
	if actor.Role != domain.RoleAdmin {
		return ErrForbidden
	}
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	if _, err := s.storage.GetGroup(ctx, groupID); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	users, err := s.storage.ListUsers(ctx)
	if err != nil {
		return err
	}
	for i := range users {
		if member(users[i].Groups, groupID) {
			users[i].Groups = remove(users[i].Groups, groupID)
			if err := s.storage.SaveUser(ctx, &users[i]); err != nil {
				return err
			}
		}
	}
	channels, err := s.storage.ListChannels(ctx)
	if err != nil {
		return err
	}
	for i := range channels {
		if !member(channels[i].Groups, groupID) {
			continue
		}
		lock := s.channelLock(channels[i].ID)
		lock.Lock()
		current, getErr := s.storage.GetChannel(ctx, channels[i].ID)
		if getErr == nil {
			current.Groups = remove(current.Groups, groupID)
			getErr = s.storage.SaveChannel(ctx, current)
		}
		lock.Unlock()
		if getErr != nil {
			return getErr
		}
	}
	if err := s.storage.DeleteGroup(ctx, groupID); err != nil {
		return err
	}
	s.publishChannelsChanged(ctx)
	return nil
}

func (s *Service) groupDetailLocked(ctx context.Context, groupID string) (*GroupDetail, error) {
	group, err := s.storage.GetGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	users, err := s.storage.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	channels, err := s.storage.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	detail := &GroupDetail{Group: group, Members: []GroupMember{}, Channels: []GroupChannel{}}
	for _, user := range users {
		if member(user.Groups, groupID) {
			detail.Members = append(detail.Members, GroupMember{ID: user.ID, Name: user.Name, Role: user.Role, Enabled: user.Enabled})
		}
	}
	for _, channel := range channels {
		if member(channel.Groups, groupID) {
			detail.Channels = append(detail.Channels, GroupChannel{ID: channel.ID, Name: channel.Name, Type: channel.Type})
		}
	}
	return detail, nil
}

func (s *Service) CreateUsers(ctx context.Context, actorID string, inputs []NewUser) ([]domain.User, error) {
	actor, err := s.user(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if actor.Role != domain.RoleAdmin {
		return nil, ErrForbidden
	}
	if len(inputs) == 0 || len(inputs) > 500 {
		return nil, fmt.Errorf("%w: ユーザーは1名以上500名以下で指定してください", ErrInvalid)
	}
	s.groupMu.RLock()
	defer s.groupMu.RUnlock()
	s.userMu.Lock()
	defer s.userMu.Unlock()
	seen := make(map[string]bool, len(inputs))
	users := make([]domain.User, len(inputs))
	for i, input := range inputs {
		input.ID, input.Name = strings.TrimSpace(input.ID), strings.TrimSpace(input.Name)
		if !validUserID.MatchString(input.ID) || strings.HasPrefix(input.ID, "ai_") || input.Name == "" || utf8.RuneCountInString(input.Name) > 100 {
			return nil, fmt.Errorf("%w: %d行目にはIDと100文字以内の名前が必要です", ErrInvalid, i+1)
		}
		if seen[input.ID] {
			return nil, fmt.Errorf("%w: ユーザーID %q が重複しています", ErrConflict, input.ID)
		}
		seen[input.ID] = true
		if input.Role == "" {
			input.Role = domain.RoleUser
		}
		if input.Role != domain.RoleUser && input.Role != domain.RoleAdmin {
			return nil, fmt.Errorf("%w: ユーザー %q の権限種別が正しくありません", ErrInvalid, input.ID)
		}
		if !validPassword(input.Password) {
			return nil, fmt.Errorf("%w: ユーザー %q のパスワードは8文字以上72バイト以内にしてください", ErrInvalid, input.ID)
		}
		for _, groupID := range unique(input.Groups) {
			if _, err := s.storage.GetGroup(ctx, groupID); errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("%w: グループ %q は存在しません", ErrInvalid, groupID)
			} else if err != nil {
				return nil, err
			}
		}
		if _, err := s.storage.GetUser(ctx, input.ID); err == nil {
			return nil, fmt.Errorf("%w: ユーザー %q はすでに存在します", ErrConflict, input.ID)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		users[i] = domain.User{Version: domain.Version, ID: input.ID, Name: input.Name, Groups: unique(input.Groups), Role: input.Role, Enabled: true, PasswordHash: string(hash)}
	}
	for i := range users {
		if err := s.storage.SaveUser(ctx, &users[i]); err != nil {
			return nil, err
		}
	}
	s.publishChannelsChanged(ctx)
	return users, nil
}

func (s *Service) UpdateUser(ctx context.Context, actorID, userID string, input UpdateUser) (*domain.User, error) {
	actor, err := s.user(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if actor.Role != domain.RoleAdmin {
		return nil, ErrForbidden
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 100 {
		return nil, fmt.Errorf("%w: 名前は必須で、100文字以内にしてください", ErrInvalid)
	}
	if input.Role != domain.RoleUser && input.Role != domain.RoleAdmin {
		return nil, fmt.Errorf("%w: 権限種別には user または admin を指定してください", ErrInvalid)
	}
	if input.Password != "" && !validPassword(input.Password) {
		return nil, fmt.Errorf("%w: パスワードは8文字以上72バイト以内にしてください", ErrInvalid)
	}

	s.userMu.Lock()
	defer s.userMu.Unlock()
	user, err := s.storage.GetUser(ctx, userID)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if user.Enabled && user.Role == domain.RoleAdmin && (!input.Enabled || input.Role != domain.RoleAdmin) {
		users, err := s.storage.ListUsers(ctx)
		if err != nil {
			return nil, err
		}
		enabledAdmins := 0
		for _, candidate := range users {
			if candidate.Enabled && candidate.Role == domain.RoleAdmin {
				enabledAdmins++
			}
		}
		if enabledAdmins <= 1 {
			return nil, fmt.Errorf("%w: 有効な最後の管理者を無効化したり一般ユーザーへ変更したりすることはできません", ErrInvalid)
		}
	}
	user.Name = input.Name
	if input.GroupsProvided && !sameIDs(user.Groups, input.Groups) {
		return nil, fmt.Errorf("%w: グループ所属はグループ管理画面から変更してください", ErrInvalid)
	}
	user.Role = input.Role
	user.Enabled = input.Enabled
	if input.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		user.PasswordHash = string(hash)
		user.AuthGeneration++
	}
	if err := s.storage.SaveUser(ctx, user); err != nil {
		return nil, err
	}
	if input.Password != "" || !user.Enabled {
		s.PasswordChanged(userID)
	}
	s.publishChannelsChanged(ctx)
	return user, nil
}

func validPassword(password string) bool {
	return utf8.RuneCountInString(password) >= MinPasswordRunes && len([]byte(password)) <= 72
}

func (s *Service) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) (uint64, error) {
	if !s.selfPasswordChange {
		return 0, ErrForbidden
	}
	if !validPassword(newPassword) {
		return 0, fmt.Errorf("%w: パスワードは8文字以上72バイト以内にしてください", ErrInvalid)
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	user, err := s.user(ctx, userID)
	if err != nil {
		return 0, err
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassword)) != nil {
		return 0, fmt.Errorf("%w: 現在のパスワードが正しくありません", ErrInvalid)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	user.PasswordHash = string(hash)
	user.AuthGeneration++
	if err := s.storage.SaveUser(ctx, user); err != nil {
		return 0, err
	}
	return user.AuthGeneration, nil
}

func (s *Service) ListChannels(ctx context.Context, userID string) ([]domain.Channel, error) {
	user, err := s.user(ctx, userID)
	if err != nil {
		return nil, err
	}
	channels, err := s.storage.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	visible := make([]domain.Channel, 0, len(channels))
	for _, channel := range channels {
		normalizeChannelManagers(&channel)
		if channel.ArchivedAt != nil {
			continue
		}
		if channel.Type == domain.ChannelPublic || channelMember(user, &channel) {
			visible = append(visible, channel)
		}
	}
	return visible, nil
}

func (s *Service) GetChannel(ctx context.Context, userID, channelID string) (*domain.Channel, error) {
	user, err := s.user(ctx, userID)
	if err != nil {
		return nil, err
	}
	channel, err := s.channel(ctx, channelID)
	if err != nil {
		return nil, err
	}
	if channel.ArchivedAt != nil {
		return nil, ErrNotFound
	}
	normalizeChannelManagers(channel)
	if channel.Type == domain.ChannelPrivate && !channelMember(user, channel) {
		return nil, ErrNotFound
	}
	return channel, nil
}

func (s *Service) CreateChannel(ctx context.Context, actorID string, channel domain.Channel) (*domain.Channel, error) {
	actor, err := s.user(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if actor.Role != domain.RoleAdmin {
		return nil, ErrForbidden
	}
	s.groupMu.RLock()
	defer s.groupMu.RUnlock()
	channel.ID = strings.TrimSpace(channel.ID)
	channel.Name = strings.TrimSpace(channel.Name)
	if channel.ID == "" || channel.Name == "" || utf8.RuneCountInString(channel.Name) > 100 {
		return nil, fmt.Errorf("%w: チャンネルIDと100文字以内の名前が必要です", ErrInvalid)
	}
	if channel.Type != domain.ChannelPublic && channel.Type != domain.ChannelPrivate {
		return nil, fmt.Errorf("%w: チャンネル種別には public または private を指定してください", ErrInvalid)
	}
	lock := s.channelLock(channel.ID)
	lock.Lock()
	defer lock.Unlock()
	if _, err := s.storage.GetChannel(ctx, channel.ID); err == nil {
		return nil, ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	channel.Version = domain.Version
	channel.CreatedBy = actorID
	channel.CreatedAt = time.Now()
	channel.Members = unique(append(channel.Members, actorID))
	channel.Groups = unique(channel.Groups)
	channel.Managers = unique(append(channel.Managers, actorID))
	for _, id := range channel.Members {
		if _, err := s.user(ctx, id); err != nil {
			return nil, fmt.Errorf("%w: メンバー %q は存在しないか、無効です", ErrInvalid, id)
		}
	}
	for _, id := range channel.Groups {
		if _, err := s.storage.GetGroup(ctx, id); err != nil {
			return nil, fmt.Errorf("%w: グループ %q は存在しません", ErrInvalid, id)
		}
	}
	for _, id := range channel.Managers {
		if _, err := s.user(ctx, id); err != nil {
			return nil, fmt.Errorf("%w: チャンネル管理者 %q は存在しないか、無効です", ErrInvalid, id)
		}
		channel.Members = unique(append(channel.Members, id))
	}
	if err := s.storage.SaveChannel(ctx, &channel); err != nil {
		return nil, err
	}
	s.publishChannelsChanged(ctx)
	return &channel, nil
}

func (s *Service) ListManageableChannels(ctx context.Context, actorID string, includeArchived bool) ([]domain.Channel, error) {
	actor, err := s.user(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if includeArchived && actor.Role != domain.RoleAdmin {
		return nil, ErrForbidden
	}
	channels, err := s.storage.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Channel, 0, len(channels))
	for i := range channels {
		normalizeChannelManagers(&channels[i])
		if channels[i].ArchivedAt != nil && !includeArchived {
			continue
		}
		if actor.Role == domain.RoleAdmin || (channels[i].ArchivedAt == nil && isChannelManager(&channels[i], actorID)) {
			out = append(out, channels[i])
		}
	}
	return out, nil
}

func (s *Service) GetChannelManagement(ctx context.Context, actorID, channelID string) (*ChannelDetail, error) {
	actor, err := s.user(ctx, actorID)
	if err != nil {
		return nil, err
	}
	channel, err := s.channel(ctx, channelID)
	if err != nil {
		return nil, err
	}
	normalizeChannelManagers(channel)
	manager := isChannelManager(channel, actorID)
	if actor.Role != domain.RoleAdmin && (!manager || channel.ArchivedAt != nil) {
		return nil, ErrForbidden
	}
	users, err := s.storage.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	direct := make(map[string]bool, len(channel.Members))
	for _, id := range channel.Members {
		direct[id] = true
	}
	groups := make(map[string]bool, len(channel.Groups))
	for _, id := range channel.Groups {
		groups[id] = true
	}
	members := make([]ChannelMember, 0, len(users))
	availableUsers := make([]ChannelUser, 0, len(users))
	for _, user := range users {
		if user.Enabled || direct[user.ID] || isChannelManager(channel, user.ID) {
			availableUsers = append(availableUsers, ChannelUser{ID: user.ID, Name: user.Name, Enabled: user.Enabled})
		}
		if !user.Enabled {
			continue
		}
		via := make([]string, 0, len(user.Groups))
		for _, groupID := range user.Groups {
			if groups[groupID] {
				via = append(via, groupID)
			}
		}
		if direct[user.ID] || len(via) > 0 {
			members = append(members, ChannelMember{ID: user.ID, Name: user.Name, Direct: direct[user.ID], Manager: isChannelManager(channel, user.ID), ViaGroups: via})
		}
	}
	admin := actor.Role == domain.RoleAdmin
	return &ChannelDetail{Channel: channel, EffectiveMembers: members, Users: availableUsers, Capabilities: ChannelCapabilities{
		ManageMembers: admin || manager, ManageSettings: admin, Archive: admin && channel.ArchivedAt == nil, Restore: admin && channel.ArchivedAt != nil,
	}}, nil
}

func (s *Service) UpdateChannel(ctx context.Context, actorID, channelID, name string, channelType domain.ChannelType) (*domain.Channel, error) {
	actor, err := s.user(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if actor.Role != domain.RoleAdmin {
		return nil, ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return nil, fmt.Errorf("%w: チャンネル名は必須で、100文字以内にしてください", ErrInvalid)
	}
	if channelType != domain.ChannelPublic && channelType != domain.ChannelPrivate {
		return nil, fmt.Errorf("%w: チャンネル種別には public または private を指定してください", ErrInvalid)
	}
	lock := s.channelLock(channelID)
	lock.Lock()
	defer lock.Unlock()
	channel, err := s.channel(ctx, channelID)
	if err != nil {
		return nil, err
	}
	if channel.ArchivedAt != nil {
		return nil, ErrNotFound
	}
	normalizeChannelManagers(channel)
	channel.Name, channel.Type = name, channelType
	if err := s.storage.SaveChannel(ctx, channel); err != nil {
		return nil, err
	}
	s.publishChannelsChanged(ctx)
	return channel, nil
}

func (s *Service) ChangeChannelMembers(ctx context.Context, actorID, channelID string, changes ChannelMemberChanges) (*ChannelDetail, error) {
	actor, err := s.user(ctx, actorID)
	if err != nil {
		return nil, err
	}
	changes.AddUsers, changes.RemoveUsers = unique(changes.AddUsers), unique(changes.RemoveUsers)
	changes.AddGroups, changes.RemoveGroups = unique(changes.AddGroups), unique(changes.RemoveGroups)
	changes.AddManagers, changes.RemoveManagers = unique(changes.AddManagers), unique(changes.RemoveManagers)
	for _, pair := range [][2][]string{{changes.AddUsers, changes.RemoveUsers}, {changes.AddGroups, changes.RemoveGroups}, {changes.AddManagers, changes.RemoveManagers}} {
		for _, id := range pair[0] {
			if member(pair[1], id) {
				return nil, fmt.Errorf("%w: %q を同時に追加・解除することはできません", ErrInvalid, id)
			}
		}
	}
	s.groupMu.RLock()
	defer s.groupMu.RUnlock()
	lock := s.channelLock(channelID)
	lock.Lock()
	channel, err := s.channel(ctx, channelID)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	normalizeChannelManagers(channel)
	if channel.ArchivedAt != nil {
		lock.Unlock()
		return nil, ErrNotFound
	}
	if actor.Role != domain.RoleAdmin && !isChannelManager(channel, actorID) {
		lock.Unlock()
		return nil, ErrForbidden
	}
	for _, id := range append(append([]string{}, changes.AddUsers...), changes.AddManagers...) {
		if _, err := s.user(ctx, id); err != nil {
			lock.Unlock()
			return nil, fmt.Errorf("%w: ユーザー %q は存在しないか、無効です", ErrInvalid, id)
		}
	}
	for _, id := range changes.AddGroups {
		if _, err := s.storage.GetGroup(ctx, id); err != nil {
			lock.Unlock()
			return nil, fmt.Errorf("%w: グループ %q は存在しません", ErrInvalid, id)
		}
	}
	for _, id := range changes.AddUsers {
		channel.Members = unique(append(channel.Members, id))
	}
	for _, id := range changes.AddManagers {
		channel.Managers = unique(append(channel.Managers, id))
		channel.Members = unique(append(channel.Members, id))
	}
	for _, id := range changes.RemoveManagers {
		channel.Managers = remove(channel.Managers, id)
	}
	if len(channel.Managers) == 0 {
		lock.Unlock()
		return nil, fmt.Errorf("%w: チャンネル管理者を1名以上設定してください", ErrInvalid)
	}
	for _, id := range changes.RemoveUsers {
		if isChannelManager(channel, id) {
			lock.Unlock()
			return nil, fmt.Errorf("%w: メンバー %q を解除する前にチャンネル管理者から外してください", ErrInvalid, id)
		}
		channel.Members = remove(channel.Members, id)
	}
	for _, id := range changes.AddGroups {
		channel.Groups = unique(append(channel.Groups, id))
	}
	for _, id := range changes.RemoveGroups {
		channel.Groups = remove(channel.Groups, id)
	}
	if err := s.storage.SaveChannel(ctx, channel); err != nil {
		lock.Unlock()
		return nil, err
	}
	lock.Unlock()
	s.publishChannelsChanged(ctx)
	if actor.Role != domain.RoleAdmin && !isChannelManager(channel, actorID) {
		return &ChannelDetail{Channel: channel}, nil
	}
	return s.GetChannelManagement(ctx, actorID, channelID)
}

func (s *Service) ArchiveChannel(ctx context.Context, actorID, channelID string) error {
	return s.setChannelArchived(ctx, actorID, channelID, true)
}

func (s *Service) RestoreChannel(ctx context.Context, actorID, channelID string) error {
	return s.setChannelArchived(ctx, actorID, channelID, false)
}

func (s *Service) setChannelArchived(ctx context.Context, actorID, channelID string, archived bool) error {
	actor, err := s.user(ctx, actorID)
	if err != nil {
		return err
	}
	if actor.Role != domain.RoleAdmin {
		return ErrForbidden
	}
	lock := s.channelLock(channelID)
	lock.Lock()
	defer lock.Unlock()
	channel, err := s.channel(ctx, channelID)
	if err != nil {
		return err
	}
	normalizeChannelManagers(channel)
	if archived {
		if channel.ArchivedAt != nil {
			return ErrConflict
		}
		now := time.Now()
		channel.ArchivedAt, channel.ArchivedBy = &now, actorID
	} else {
		if channel.ArchivedAt == nil {
			return ErrConflict
		}
		channel.ArchivedAt, channel.ArchivedBy = nil, ""
	}
	if err := s.storage.SaveChannel(ctx, channel); err != nil {
		return err
	}
	s.publishChannelsChanged(ctx)
	return nil
}

func (s *Service) publishChannelsChanged(ctx context.Context) {
	s.presenceMu.Lock()
	clear(s.presenceMembers)
	s.presenceMu.Unlock()
	if publisher, ok := s.publisher.(ChannelPublisher); ok {
		publisher.ChannelsChanged(ctx)
	}
}

func (s *Service) JoinChannel(ctx context.Context, userID, channelID string) (*domain.Channel, error) {
	return s.changePublicMembership(ctx, userID, channelID, true)
}

func (s *Service) LeaveChannel(ctx context.Context, userID, channelID string) (*domain.Channel, error) {
	return s.changePublicMembership(ctx, userID, channelID, false)
}

func (s *Service) changePublicMembership(ctx context.Context, userID, channelID string, join bool) (*domain.Channel, error) {
	user, err := s.user(ctx, userID)
	if err != nil {
		return nil, err
	}
	lock := s.channelLock(channelID)
	lock.Lock()
	defer lock.Unlock()
	channel, err := s.channel(ctx, channelID)
	if err != nil {
		return nil, err
	}
	if channel.Type != domain.ChannelPublic {
		return nil, ErrForbidden
	}
	if channel.ArchivedAt != nil {
		return nil, ErrNotFound
	}
	normalizeChannelManagers(channel)
	if join {
		if !channelMember(user, channel) {
			latest, err := s.storage.LatestMessageSeq(ctx, channelID)
			if err != nil {
				return nil, err
			}
			if err := s.storage.SetReadState(ctx, userID, channelID, latest); err != nil {
				return nil, err
			}
		}
		channel.Members = unique(append(channel.Members, userID))
	} else {
		if isChannelManager(channel, userID) {
			return nil, fmt.Errorf("%w: チャンネル管理者はチャンネルから退出できません", ErrInvalid)
		}
		channel.Members = remove(channel.Members, userID)
	}
	if err := s.storage.SaveChannel(ctx, channel); err != nil {
		return nil, err
	}
	s.publishChannelsChanged(ctx)
	return channel, nil
}

func (s *Service) GetMessages(ctx context.Context, userID, channelID string, query storage.MessageQuery) (storage.MessagePage, error) {
	if err := s.canRead(ctx, userID, channelID); err != nil {
		return storage.MessagePage{}, err
	}
	if query.Limit == 0 {
		query.Limit = 100
	}
	if query.Limit < 1 || query.Limit > 500 {
		return storage.MessagePage{}, fmt.Errorf("%w: 取得件数は1以上500以下で指定してください", ErrInvalid)
	}
	cursors := 0
	if query.BeforeSeq != nil {
		cursors++
	}
	if query.AfterSeq != nil {
		cursors++
	}
	if query.AroundSeq != nil {
		cursors++
	}
	if cursors > 1 {
		return storage.MessagePage{}, fmt.Errorf("%w: before、after、around は同時に指定できません", ErrInvalid)
	}
	if query.BeforeSeq != nil && *query.BeforeSeq < 1 {
		return storage.MessagePage{}, fmt.Errorf("%w: before には1以上の値を指定してください", ErrInvalid)
	}
	if query.AfterSeq != nil && *query.AfterSeq < 0 {
		return storage.MessagePage{}, fmt.Errorf("%w: after には0以上の値を指定してください", ErrInvalid)
	}
	if query.AroundSeq != nil && *query.AroundSeq < 1 {
		return storage.MessagePage{}, fmt.Errorf("%w: around には1以上の値を指定してください", ErrInvalid)
	}
	query.ThreadRootSeq = 0
	page, err := s.storage.GetMessages(ctx, channelID, query)
	if err != nil {
		return page, err
	}
	for i := range page.Messages {
		page.Messages[i], err = s.publicMessage(ctx, channelID, page.Messages[i])
		if err != nil {
			return storage.MessagePage{}, err
		}
	}
	roots := make([]int64, 0, len(page.Messages))
	for _, m := range page.Messages {
		roots = append(roots, m.Seq)
	}
	if len(roots) == 0 {
		return page, nil
	}
	summaries, err := s.storage.ThreadSummaries(ctx, userID, channelID, storage.ThreadFilter{Roots: roots})
	if err != nil {
		return page, err
	}
	visible := make(map[int64]bool, len(page.Messages))
	for _, m := range page.Messages {
		visible[m.Seq] = true
	}
	page.Threads = make(map[int64]storage.ThreadSummary)
	for _, t := range summaries {
		if visible[t.RootSeq] {
			page.Threads[t.RootSeq] = t
		}
	}
	return page, nil
}

func hasAIMention(text string) bool {
	for _, match := range mentionPattern.FindAllStringSubmatch(text, -1) {
		if match[1] == "ai" {
			return true
		}
	}
	return false
}

func (s *Service) PostMessage(ctx context.Context, userID, channelID, text string, attachmentIDs ...string) (domain.Message, error) {
	if err := s.canPost(ctx, userID, channelID); err != nil {
		return domain.Message{}, err
	}
	if strings.TrimSpace(text) == "" || len([]byte(text)) > MaxMessageBytes {
		return domain.Message{}, fmt.Errorf("%w: メッセージは空にせず、%dバイト以内にしてください", ErrInvalid, MaxMessageBytes)
	}
	var attachments []domain.Attachment
	if len(attachmentIDs) > 0 {
		if !hasAIMention(text) {
			return domain.Message{}, fmt.Errorf("%w: ファイル添付はAIへのメンション（@ai:...）時のみ利用できます", ErrInvalid)
		}
		var err error
		attachments, err = s.consumeStagedAttachments(userID, channelID, attachmentIDs)
		if err != nil {
			return domain.Message{}, err
		}
	}
	recipients, err := s.mentionRecipients(ctx, userID, channelID, text)
	if err != nil {
		return domain.Message{}, err
	}
	message, err := s.storage.AddMessage(ctx, channelID, domain.Message{UserID: userID, Text: text, MentionUserIDs: recipients, Attachments: attachments})
	if err != nil {
		return domain.Message{}, err
	}
	readErr := s.storage.SetReadState(ctx, userID, channelID, message.Seq)
	if s.publisher != nil {
		s.publisher.NewMessage(ctx, channelID, message.Seq)
	}
	s.enqueueAI(userID, channelID, message)
	if readErr != nil {
		return message, &CommittedMessageError{Err: fmt.Errorf("advance author read state: %w", readErr)}
	}
	return message, nil
}

// CommittedMessageError reports an auxiliary failure after a message was durably appended.
// Callers must not retry the post, because doing so would create a duplicate message.
type CommittedMessageError struct{ Err error }

func (e *CommittedMessageError) Error() string { return e.Err.Error() }
func (e *CommittedMessageError) Unwrap() error { return e.Err }

var mentionPattern = regexp.MustCompile(`(?:^|[[:space:]])@(?:(group|ai):)?([A-Za-z0-9][A-Za-z0-9_-]{0,63})`)

func (s *Service) mentionRecipients(ctx context.Context, authorID, channelID, text string) ([]string, error) {
	users, err := s.storage.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	channel, err := s.channel(ctx, channelID)
	if err != nil {
		return nil, err
	}
	wantedUsers, wantedGroups := map[string]bool{}, map[string]bool{}
	for _, match := range mentionPattern.FindAllStringSubmatch(text, -1) {
		if match[1] == "ai" {
			continue
		}
		if match[1] == "group" {
			wantedGroups[match[2]] = true
		} else {
			wantedUsers[match[2]] = true
		}
	}
	result := []string{}
	for i := range users {
		u := &users[i]
		if !u.Enabled || u.ID == authorID {
			continue
		}
		mentioned := wantedUsers[u.ID]
		for _, group := range u.Groups {
			if wantedGroups[group] {
				mentioned = true
			}
		}
		if !mentioned || channel.Type == domain.ChannelPrivate && !channelMember(u, channel) {
			continue
		}
		result = append(result, u.ID)
	}
	sort.Strings(result)
	return result, nil
}

func (s *Service) ListMentions(ctx context.Context, userID, cursor string, limit int) (storage.MentionPage, error) {
	if _, err := s.user(ctx, userID); err != nil {
		return storage.MentionPage{}, err
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return storage.MentionPage{}, fmt.Errorf("%w: 取得件数は1以上100以下で指定してください", ErrInvalid)
	}
	mentions, err := s.storage.ListMentions(ctx, userID)
	if err != nil {
		return storage.MentionPage{}, err
	}
	channels, err := s.ListChannels(ctx, userID)
	if err != nil {
		return storage.MentionPage{}, err
	}
	readable := make(map[string]bool, len(channels))
	for _, channel := range channels {
		readable[channel.ID] = true
	}
	visible := make([]domain.Mention, 0, len(mentions))
	unread := 0
	cutoff := time.Now().Add(-s.mentionRetention)
	for _, mention := range mentions {
		if !readable[mention.ChannelID] {
			continue
		}
		if withdrawn, err := s.messageWithdrawal(ctx, mention.ChannelID, mention.Message); err != nil {
			return storage.MentionPage{}, err
		} else if withdrawn != nil {
			continue
		}
		if mention.ReadAt == nil {
			unread++
		} else if mention.Message.Timestamp.Before(cutoff) {
			continue
		}
		visible = append(visible, mention)
	}
	offset := 0
	if cursor != "" {
		found := false
		for i := range visible {
			if visible[i].Message.ID == cursor {
				offset, found = i+1, true
				break
			}
		}
		if !found {
			return storage.MentionPage{}, fmt.Errorf("%w: カーソルが正しくありません", ErrInvalid)
		}
	}
	if offset > len(visible) {
		offset = len(visible)
	}
	end := offset + limit
	if end > len(visible) {
		end = len(visible)
	}
	page := storage.MentionPage{Mentions: visible[offset:end], UnreadCount: unread}
	if end < len(visible) {
		page.NextCursor = visible[end-1].Message.ID
	}
	return page, nil
}

func (s *Service) MarkMentionRead(ctx context.Context, userID, messageID string) error {
	mentions, err := s.storage.ListMentions(ctx, userID)
	if err != nil {
		return err
	}
	for _, mention := range mentions {
		if mention.Message.ID == messageID {
			if err := s.canRead(ctx, userID, mention.ChannelID); err != nil {
				return err
			}
			return s.storage.SetMentionRead(ctx, userID, messageID, time.Now())
		}
	}
	return ErrNotFound
}

func (s *Service) GetReactions(ctx context.Context, userID, channelID string, afterMessageSeq int64) ([]MessageReactions, error) {
	if err := s.canRead(ctx, userID, channelID); err != nil {
		return nil, err
	}
	if afterMessageSeq < 0 {
		return nil, fmt.Errorf("%w: afterMessageSeq には0以上の値を指定してください", ErrInvalid)
	}
	states, err := s.storage.GetReactions(ctx, channelID, afterMessageSeq)
	if err != nil {
		return nil, err
	}
	return reactionViews(states, userID), nil
}

func (s *Service) GetReactionsForMessages(ctx context.Context, userID, channelID string, seqs []int64) ([]MessageReactions, error) {
	if err := s.canRead(ctx, userID, channelID); err != nil {
		return nil, err
	}
	if len(seqs) == 0 || len(seqs) > 101 {
		return nil, fmt.Errorf("%w: メッセージ番号は1件から101件まで指定してください", ErrInvalid)
	}
	seen := make(map[int64]bool, len(seqs))
	for _, seq := range seqs {
		if seq < 1 || seen[seq] {
			return nil, fmt.Errorf("%w: メッセージ番号が正しくありません", ErrInvalid)
		}
		seen[seq] = true
	}
	states, err := s.storage.GetReactionsForMessages(ctx, channelID, seqs)
	if err != nil {
		return nil, err
	}
	return reactionViews(states, userID), nil
}

func (s *Service) GetReactionUsers(ctx context.Context, userID, channelID string, messageSeq int64, key string) ([]string, error) {
	if err := s.canRead(ctx, userID, channelID); err != nil {
		return nil, err
	}
	if messageSeq < 1 {
		return nil, fmt.Errorf("%w: メッセージ番号には1以上の値を指定してください", ErrInvalid)
	}
	if !reactionKeys[key] {
		return nil, fmt.Errorf("%w: 対応していないリアクションです", ErrInvalid)
	}
	return s.storage.GetReactionUsers(ctx, channelID, messageSeq, key)
}

func (s *Service) SetReaction(ctx context.Context, userID, channelID string, messageSeq int64, key string, active bool) (MessageReactions, error) {
	lock := s.channelLock("withdraw:message:" + channelID + ":" + strconv.FormatInt(messageSeq, 10))
	lock.Lock()
	defer lock.Unlock()
	if !reactionKeys[key] {
		return MessageReactions{}, fmt.Errorf("%w: 対応していないリアクションです", ErrInvalid)
	}
	if messageSeq < 1 {
		return MessageReactions{}, fmt.Errorf("%w: メッセージ番号には1以上の値を指定してください", ErrInvalid)
	}
	if err := s.canPost(ctx, userID, channelID); err != nil {
		return MessageReactions{}, err
	}
	m, err := s.storage.GetMessage(ctx, channelID, messageSeq)
	if errors.Is(err, os.ErrNotExist) {
		return MessageReactions{}, ErrNotFound
	}
	if err != nil {
		return MessageReactions{}, err
	}
	if withdrawn, err := s.messageWithdrawal(ctx, channelID, m); err != nil {
		return MessageReactions{}, err
	} else if withdrawn != nil {
		return MessageReactions{}, ErrInvalid
	}
	changed, err := s.storage.SetReaction(ctx, channelID, domain.ReactionEvent{
		MessageSeq: messageSeq, UserID: userID, Key: key, Active: active,
	})
	if err != nil {
		return MessageReactions{}, err
	}
	if changed {
		if publisher, ok := s.publisher.(ReactionPublisher); ok {
			publisher.ReactionChanged(ctx, channelID, messageSeq)
		}
	}
	states, err := s.storage.GetReactions(ctx, channelID, messageSeq-1)
	if err != nil {
		return MessageReactions{}, err
	}
	for _, view := range reactionViews(states, userID) {
		if view.MessageSeq == messageSeq {
			return view, nil
		}
	}
	return MessageReactions{MessageSeq: messageSeq, Reactions: []ReactionSummary{}}, nil
}

func reactionViews(states []domain.ReactionState, userID string) []MessageReactions {
	byMessage := make(map[int64][]ReactionSummary)
	for _, state := range states {
		reacted := false
		for _, id := range state.UserIDs {
			if id == userID {
				reacted = true
				break
			}
		}
		byMessage[state.MessageSeq] = append(byMessage[state.MessageSeq], ReactionSummary{
			Key: state.Key, Count: len(state.UserIDs), ReactedByMe: reacted,
		})
	}
	seqs := make([]int64, 0, len(byMessage))
	for seq := range byMessage {
		seqs = append(seqs, seq)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	out := make([]MessageReactions, 0, len(seqs))
	for _, seq := range seqs {
		summaries := byMessage[seq]
		sort.Slice(summaries, func(i, j int) bool { return summaries[i].Key < summaries[j].Key })
		out = append(out, MessageReactions{MessageSeq: seq, Reactions: summaries})
	}
	return out
}

// CanReadChannel is the authorization boundary used by realtime delivery.
func (s *Service) CanReadChannel(ctx context.Context, userID, channelID string) bool {
	return s.canRead(ctx, userID, channelID) == nil
}

// CanPostChannel is the authorization boundary used for client-originated realtime events.
func (s *Service) CanPostChannel(ctx context.Context, userID, channelID string) bool {
	return s.canPost(ctx, userID, channelID) == nil
}

func (s *Service) MarkRead(ctx context.Context, userID, channelID string, seq int64) error {
	if err := s.canRead(ctx, userID, channelID); err != nil {
		return err
	}
	if seq < 0 {
		return fmt.Errorf("%w: 既読位置には0以上の値を指定してください", ErrInvalid)
	}
	if seq > 0 {
		after := seq - 1
		page, err := s.storage.GetMessages(ctx, channelID, storage.MessageQuery{AfterSeq: &after, Limit: 1})
		if err != nil {
			return err
		}
		if len(page.Messages) == 0 || page.Messages[0].Seq != seq {
			return fmt.Errorf("%w: 指定されたメッセージ番号は存在しません", ErrInvalid)
		}
	}
	return s.storage.SetReadState(ctx, userID, channelID, seq)
}

func (s *Service) GetReadStatus(ctx context.Context, userID, channelID string) (ReadStatus, error) {
	channel, err := s.GetChannel(ctx, userID, channelID)
	if err != nil {
		return ReadStatus{}, err
	}
	user, err := s.user(ctx, userID)
	if err != nil {
		return ReadStatus{}, err
	}
	lastRead, err := s.storage.GetReadState(ctx, userID, channelID)
	if err != nil {
		return ReadStatus{}, err
	}
	return s.readStatus(ctx, userID, channelID, lastRead, channelMember(user, channel))
}
func (s *Service) readStatus(ctx context.Context, userID, channelID string, lastRead int64, joined bool) (ReadStatus, error) {
	latest, unread, err := s.storage.MessageReadCounts(ctx, channelID, lastRead)
	if err != nil {
		return ReadStatus{}, err
	}
	if !joined {
		return ReadStatus{LastReadSeq: lastRead, LatestSeq: latest}, nil
	}
	summaries, err := s.storage.ThreadSummaries(ctx, userID, channelID)
	if err != nil {
		return ReadStatus{}, err
	}
	updates := false
	for _, t := range summaries {
		if t.UnreadCount > 0 {
			updates = true
			break
		}
	}
	return ReadStatus{LastReadSeq: lastRead, LatestSeq: latest, UnreadCount: unread, ThreadUpdates: updates}, nil
}
func (s *Service) GetReadStatuses(ctx context.Context, userID string) (map[string]ReadStatus, error) {
	user, err := s.user(ctx, userID)
	if err != nil {
		return nil, err
	}
	channels, err := s.ListChannels(ctx, userID)
	if err != nil {
		return nil, err
	}
	states, err := s.storage.GetReadStates(ctx, userID)
	if err != nil {
		return nil, err
	}
	statuses := make(map[string]ReadStatus, len(channels))
	for _, c := range channels {
		status, err := s.readStatus(ctx, userID, c.ID, states[c.ID], channelMember(user, &c))
		if err != nil {
			return nil, err
		}
		statuses[c.ID] = status
	}
	return statuses, nil
}

func (s *Service) canRead(ctx context.Context, userID, channelID string) error {
	user, err := s.user(ctx, userID)
	if err != nil {
		return err
	}
	channel, err := s.channel(ctx, channelID)
	if err != nil {
		return err
	}
	if channel.ArchivedAt != nil {
		return ErrNotFound
	}
	if channel.Type == domain.ChannelPrivate && !channelMember(user, channel) {
		return ErrNotFound
	}
	return nil
}

func (s *Service) canPost(ctx context.Context, userID, channelID string) error {
	user, err := s.user(ctx, userID)
	if err != nil {
		return err
	}
	channel, err := s.channel(ctx, channelID)
	if err != nil {
		return err
	}
	if channel.ArchivedAt != nil {
		return ErrNotFound
	}
	if channelMember(user, channel) {
		return nil
	}
	if channel.Type == domain.ChannelPrivate {
		return ErrNotFound
	}
	return ErrForbidden
}

func (s *Service) channel(ctx context.Context, id string) (*domain.Channel, error) {
	channel, err := s.storage.GetChannel(ctx, id)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return channel, err
}

func (s *Service) channelLock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks[id] == nil {
		s.locks[id] = new(sync.Mutex)
	}
	return s.locks[id]
}

func member(members []string, id string) bool {
	for _, memberID := range members {
		if memberID == id {
			return true
		}
	}
	return false
}

func channelMember(user *domain.User, channel *domain.Channel) bool {
	if member(channel.Members, user.ID) {
		return true
	}
	for _, groupID := range channel.Groups {
		if member(user.Groups, groupID) {
			return true
		}
	}
	return false
}

func normalizeChannelManagers(channel *domain.Channel) {
	if len(channel.Managers) == 0 && channel.CreatedBy != "" {
		channel.Managers = []string{channel.CreatedBy}
	} else {
		channel.Managers = unique(channel.Managers)
	}
}

func isChannelManager(channel *domain.Channel, userID string) bool {
	if member(channel.Managers, userID) {
		return true
	}
	return len(channel.Managers) == 0 && channel.CreatedBy == userID
}

func unique(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func remove(ids []string, target string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != target {
			out = append(out, id)
		}
	}
	return out
}

func sameIDs(left, right []string) bool {
	left, right = unique(left), unique(right)
	if len(left) != len(right) {
		return false
	}
	for _, id := range left {
		if !member(right, id) {
			return false
		}
	}
	return true
}
