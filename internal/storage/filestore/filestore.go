package filestore

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
)

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

type FileStorage struct {
	root            string
	positionIndexes map[string]*positionIndex

	mu              sync.Mutex
	writers         map[string]*sync.Mutex
	readLock        map[string]*sync.Mutex
	reactMu         sync.Mutex
	reactions       map[string]map[int64]map[string]map[string]bool
	reactionsLoaded map[string]bool
	mentionMu       sync.Mutex
	mentionRecords  map[string]domain.Mention
	mentionUserRefs map[string][]string
	legacyUserRefs  map[string][]string
	legacyGroupRefs map[string][]string
	scheduleWriters map[string]*sync.Mutex
}

func New(root string) (*FileStorage, error) {
	if root == "" {
		return nil, errors.New("data directory is required")
	}
	for _, dir := range []string{"users", "groups", "channels", "state", "sessions", "schedules"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			return nil, fmt.Errorf("create %s directory: %w", dir, err)
		}
	}
	return &FileStorage{
		root: root, positionIndexes: make(map[string]*positionIndex), writers: make(map[string]*sync.Mutex), readLock: make(map[string]*sync.Mutex),
		reactions:       make(map[string]map[int64]map[string]map[string]bool),
		reactionsLoaded: make(map[string]bool),
		scheduleWriters: make(map[string]*sync.Mutex),
		mentionRecords:  make(map[string]domain.Mention), mentionUserRefs: make(map[string][]string),
		legacyUserRefs: make(map[string][]string), legacyGroupRefs: make(map[string][]string),
	}, nil
}

func checkID(kind, id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("invalid %s id %q", kind, id)
	}
	return nil
}

func (s *FileStorage) keyedLock(m map[string]*sync.Mutex, key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m[key] == nil {
		m[key] = new(sync.Mutex)
	}
	return m[key]
}

func (s *FileStorage) AddMessage(ctx context.Context, channelID string, msg domain.Message) (domain.Message, error) {
	if err := checkID("channel", channelID); err != nil {
		return domain.Message{}, err
	}
	if err := checkID("user", msg.UserID); err != nil {
		return domain.Message{}, err
	}
	if msg.Text == "" {
		return domain.Message{}, errors.New("message text is required")
	}
	if err := ctx.Err(); err != nil {
		return domain.Message{}, err
	}

	lock := s.keyedLock(s.writers, channelID)
	lock.Lock()
	defer lock.Unlock()
	idx, err := s.positionsLocked(ctx, channelID)
	if err != nil {
		return domain.Message{}, err
	}
	if msg.ThreadRootSeq < 0 {
		return domain.Message{}, errors.New("invalid thread root")
	}
	if msg.ThreadRootSeq > 0 {
		root, ok := idx.bySeq[msg.ThreadRootSeq]
		if !ok || root.root != 0 {
			return domain.Message{}, os.ErrNotExist
		}
	}
	if msg.ScheduleRef != nil {
		existing, err := s.findScheduleMessage(ctx, channelID, msg.ScheduleRef)
		if err != nil {
			return domain.Message{}, err
		}
		if existing != nil {
			return *existing, nil
		}
	}
	if msg.PollRef != nil {
		if err := checkID("poll", msg.PollRef.ID); err != nil {
			return domain.Message{}, err
		}
		if existing, err := s.findPollMessage(ctx, channelID, msg.PollRef.ID); err != nil {
			return domain.Message{}, err
		} else if existing != nil {
			return *existing, nil
		}
	}

	msg.Seq = idx.latest + 1
	if msg.ID == "" {
		msg.ID, err = randomID()
		if err != nil {
			return domain.Message{}, err
		}
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}

	dir := filepath.Join(s.root, "channels", channelID, "messages")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return domain.Message{}, fmt.Errorf("create message directory: %w", err)
	}
	path := filepath.Join(dir, msg.Timestamp.Format("2006-01-02")+".jsonl")
	if err := truncateIncompleteTail(path); err != nil {
		return domain.Message{}, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return domain.Message{}, fmt.Errorf("open message log: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return domain.Message{}, err
	}
	b, err := json.Marshal(msg)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		// The line may have reached disk despite a sync/close failure. Rebuild
		// before the next operation rather than reusing an uncertain sequence.
		s.mu.Lock()
		delete(s.positionIndexes, channelID)
		s.mu.Unlock()
		return domain.Message{}, fmt.Errorf("commit message: %w", err)
	}
	idx.add(msg, messagePosition{path: path, offset: info.Size(), length: len(b) + 1, seq: msg.Seq, root: msg.ThreadRootSeq, author: msg.UserID})
	if len(msg.MentionUserIDs) > 0 || msg.MentionUserIDs == nil && legacyMentionPattern.MatchString(msg.Text) {
		s.mentionMu.Lock()
		s.indexMentionLocked(domain.Mention{Message: msg, ChannelID: channelID})
		s.mentionMu.Unlock()
	}
	return msg, nil
}

func (s *FileStorage) findScheduleMessage(ctx context.Context, channelID string, ref *domain.ScheduleReference) (*domain.Message, error) {
	after := int64(0)
	for {
		idx, err := s.positionsLocked(ctx, channelID)
		if err != nil {
			return nil, err
		}
		page, err := s.positionPage(ctx, idx, storage.MessageQuery{AfterSeq: &after, Limit: 500})
		if err != nil {
			return nil, err
		}
		for i := range page.Messages {
			candidate := &page.Messages[i]
			if candidate.ScheduleRef != nil && candidate.ScheduleRef.ID == ref.ID && candidate.ScheduleRef.Event == ref.Event {
				return candidate, nil
			}
		}
		if page.NextAfter == nil {
			return nil, nil
		}
		after = *page.NextAfter
	}
}

func truncateIncompleteTail(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open message log for recovery: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return err
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, info.Size()-1); err != nil {
		return fmt.Errorf("inspect message log tail: %w", err)
	}
	if last[0] == '\n' {
		return nil
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("read message log for recovery: %w", err)
	}
	lastNewline := -1
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '\n' {
			lastNewline = i
			break
		}
	}
	if err := f.Truncate(int64(lastNewline + 1)); err != nil {
		return fmt.Errorf("truncate incomplete message tail: %w", err)
	}
	return f.Sync()
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate message id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *FileStorage) messageFiles(channelID string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(s.root, "channels", channelID, "messages", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func (s *FileStorage) LatestMessageSeq(ctx context.Context, channelID string) (int64, error) {
	if err := checkID("channel", channelID); err != nil {
		return 0, err
	}
	lock := s.keyedLock(s.writers, channelID)
	lock.Lock()
	defer lock.Unlock()
	idx, err := s.positionsLocked(ctx, channelID)
	if err != nil {
		return 0, err
	}
	return idx.latest, nil
}

func (s *FileStorage) reactionFiles(channelID string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(s.root, "channels", channelID, "reactions", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func (s *FileStorage) loadReactionsLocked(channelID string) (map[int64]map[string]map[string]bool, error) {
	if s.reactionsLoaded[channelID] {
		return s.reactions[channelID], nil
	}
	state := make(map[int64]map[string]map[string]bool)
	files, err := s.reactionFiles(channelID)
	if err != nil {
		return nil, fmt.Errorf("list reaction logs: %w", err)
	}
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		r := bufio.NewReader(f)
		for line := 1; ; line++ {
			b, readErr := r.ReadBytes('\n')
			if len(b) > 0 {
				var event domain.ReactionEvent
				if err := json.Unmarshal(b, &event); err != nil {
					if errors.Is(readErr, io.EOF) {
						break
					}
					_ = f.Close()
					return nil, fmt.Errorf("corrupt reaction log %s line %d: %w", path, line, err)
				}
				applyReaction(state, event)
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				_ = f.Close()
				return nil, fmt.Errorf("read reaction log %s: %w", path, readErr)
			}
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	s.reactions[channelID] = state
	s.reactionsLoaded[channelID] = true
	return state, nil
}

func applyReaction(state map[int64]map[string]map[string]bool, event domain.ReactionEvent) {
	if state[event.MessageSeq] == nil {
		state[event.MessageSeq] = make(map[string]map[string]bool)
	}
	if state[event.MessageSeq][event.Key] == nil {
		state[event.MessageSeq][event.Key] = make(map[string]bool)
	}
	if event.Active {
		state[event.MessageSeq][event.Key][event.UserID] = true
	} else {
		delete(state[event.MessageSeq][event.Key], event.UserID)
	}
}

func (s *FileStorage) SetReaction(ctx context.Context, channelID string, event domain.ReactionEvent) (bool, error) {
	if err := checkID("channel", channelID); err != nil {
		return false, err
	}
	if err := checkID("user", event.UserID); err != nil {
		return false, err
	}
	if event.MessageSeq < 1 || event.Key == "" {
		return false, errors.New("reaction requires a message sequence and key")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	writer := s.keyedLock(s.writers, channelID)
	writer.Lock()
	defer writer.Unlock()
	s.reactMu.Lock()
	defer s.reactMu.Unlock()
	state, err := s.loadReactionsLocked(channelID)
	if err != nil {
		return false, err
	}
	current := state[event.MessageSeq] != nil && state[event.MessageSeq][event.Key] != nil && state[event.MessageSeq][event.Key][event.UserID]
	if current == event.Active {
		return false, nil
	}
	event.Version = domain.Version
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	dir := filepath.Join(s.root, "channels", channelID, "reactions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("create reaction directory: %w", err)
	}
	path := filepath.Join(dir, event.Timestamp.Format("2006-01-02")+".jsonl")
	if err := truncateIncompleteTail(path); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return false, fmt.Errorf("open reaction log: %w", err)
	}
	b, err := json.Marshal(event)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, fmt.Errorf("commit reaction: %w", err)
	}
	applyReaction(state, event)
	return true, nil
}

func (s *FileStorage) GetReactions(ctx context.Context, channelID string, afterMessageSeq int64) ([]domain.ReactionState, error) {
	if err := checkID("channel", channelID); err != nil {
		return nil, err
	}
	if afterMessageSeq < 0 {
		return nil, errors.New("after message sequence must not be negative")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.reactMu.Lock()
	defer s.reactMu.Unlock()
	state, err := s.loadReactionsLocked(channelID)
	if err != nil {
		return nil, err
	}
	var out []domain.ReactionState
	for seq, keys := range state {
		if seq <= afterMessageSeq {
			continue
		}
		for key, users := range keys {
			if len(users) == 0 {
				continue
			}
			userIDs := make([]string, 0, len(users))
			for userID := range users {
				userIDs = append(userIDs, userID)
			}
			sort.Strings(userIDs)
			out = append(out, domain.ReactionState{MessageSeq: seq, Key: key, UserIDs: userIDs})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MessageSeq == out[j].MessageSeq {
			return out[i].Key < out[j].Key
		}
		return out[i].MessageSeq < out[j].MessageSeq
	})
	return out, nil
}

func (s *FileStorage) GetReactionsForMessages(ctx context.Context, channelID string, seqs []int64) ([]domain.ReactionState, error) {
	if err := checkID("channel", channelID); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.reactMu.Lock()
	defer s.reactMu.Unlock()
	state, err := s.loadReactionsLocked(channelID)
	if err != nil {
		return nil, err
	}
	var out []domain.ReactionState
	for _, seq := range seqs {
		if seq < 1 {
			return nil, errors.New("invalid message sequence")
		}
		for key, users := range state[seq] {
			if len(users) == 0 {
				continue
			}
			userIDs := make([]string, 0, len(users))
			for userID := range users {
				userIDs = append(userIDs, userID)
			}
			sort.Strings(userIDs)
			out = append(out, domain.ReactionState{MessageSeq: seq, Key: key, UserIDs: userIDs})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MessageSeq == out[j].MessageSeq {
			return out[i].Key < out[j].Key
		}
		return out[i].MessageSeq < out[j].MessageSeq
	})
	return out, nil
}

func (s *FileStorage) GetReactionUsers(ctx context.Context, channelID string, messageSeq int64, key string) ([]string, error) {
	if err := checkID("channel", channelID); err != nil {
		return nil, err
	}
	if messageSeq < 1 || key == "" {
		return nil, errors.New("reaction requires a message sequence and key")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.reactMu.Lock()
	defer s.reactMu.Unlock()
	state, err := s.loadReactionsLocked(channelID)
	if err != nil {
		return nil, err
	}
	users := state[messageSeq][key]
	userIDs := make([]string, 0, len(users))
	for userID := range users {
		userIDs = append(userIDs, userID)
	}
	sort.Strings(userIDs)
	return userIDs, nil
}

func previousNewline(f *os.File, before int64) (int64, error) {
	const chunkSize = int64(4096)
	buffer := make([]byte, chunkSize)
	for before > 0 {
		start := before - chunkSize
		if start < 0 {
			start = 0
		}
		chunk := buffer[:before-start]
		if _, err := f.ReadAt(chunk, start); err != nil {
			return -1, err
		}
		for i := len(chunk) - 1; i >= 0; i-- {
			if chunk[i] == '\n' {
				return start + int64(i), nil
			}
		}
		before = start
	}
	return -1, nil
}

func (s *FileStorage) GetMessages(ctx context.Context, channelID string, query storage.MessageQuery) (storage.MessagePage, error) {
	if err := checkID("channel", channelID); err != nil {
		return storage.MessagePage{}, err
	}
	lock := s.keyedLock(s.writers, channelID)
	lock.Lock()
	defer lock.Unlock()
	idx, err := s.positionsLocked(ctx, channelID)
	if err != nil {
		return storage.MessagePage{}, err
	}
	return s.positionPage(ctx, idx, query)
}

func (s *FileStorage) GetUser(ctx context.Context, id string) (*domain.User, error) {
	if err := checkID("user", id); err != nil {
		return nil, err
	}
	var user domain.User
	if err := readJSON(ctx, filepath.Join(s.root, "users", id+".json"), &user); err != nil {
		return nil, err
	}
	return &user, nil
}

func (s *FileStorage) SaveUser(ctx context.Context, user *domain.User) error {
	if user == nil {
		return errors.New("user is required")
	}
	if err := checkID("user", user.ID); err != nil {
		return err
	}
	if user.Version == 0 {
		user.Version = domain.Version
	}
	return atomicJSON(ctx, filepath.Join(s.root, "users", user.ID+".json"), user)
}

func (s *FileStorage) ListUsers(ctx context.Context) ([]domain.User, error) {
	paths, err := filepath.Glob(filepath.Join(s.root, "users", "*.json"))
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	sort.Strings(paths)
	out := make([]domain.User, 0, len(paths))
	for _, path := range paths {
		var user domain.User
		if err := readJSON(ctx, path, &user); err != nil {
			return nil, fmt.Errorf("read user %s: %w", filepath.Base(path), err)
		}
		out = append(out, user)
	}
	return out, nil
}

func (s *FileStorage) GetGroup(ctx context.Context, id string) (*domain.Group, error) {
	if err := checkID("group", id); err != nil {
		return nil, err
	}
	var group domain.Group
	if err := readJSON(ctx, filepath.Join(s.root, "groups", id+".json"), &group); err != nil {
		return nil, err
	}
	return &group, nil
}

func (s *FileStorage) SaveGroup(ctx context.Context, group *domain.Group) error {
	if group == nil {
		return errors.New("group is required")
	}
	if err := checkID("group", group.ID); err != nil {
		return err
	}
	if group.Version == 0 {
		group.Version = domain.Version
	}
	return atomicJSON(ctx, filepath.Join(s.root, "groups", group.ID+".json"), group)
}

func (s *FileStorage) ListGroups(ctx context.Context) ([]domain.Group, error) {
	paths, err := filepath.Glob(filepath.Join(s.root, "groups", "*.json"))
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	sort.Strings(paths)
	out := make([]domain.Group, 0, len(paths))
	for _, path := range paths {
		var group domain.Group
		if err := readJSON(ctx, path, &group); err != nil {
			return nil, fmt.Errorf("read group %s: %w", filepath.Base(path), err)
		}
		out = append(out, group)
	}
	return out, nil
}

func (s *FileStorage) DeleteGroup(ctx context.Context, id string) error {
	if err := checkID("group", id); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(s.root, "groups", id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// DeleteExpiredSessions runs before serving requests. Errors omit session paths and contents.
func (s *FileStorage) DeleteExpiredSessions(ctx context.Context, now time.Time) (int, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "sessions"))
	if err != nil {
		return 0, errors.New("cannot list sessions for cleanup")
	}
	deleted, failed := 0, 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(s.root, "sessions", entry.Name())
		var session domain.Session
		if err := readJSON(ctx, path, &session); err != nil || session.ExpiresAt.IsZero() {
			failed++
			continue
		}
		if session.ExpiresAt.After(now) {
			continue
		}
		if err := os.Remove(path); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				failed++
			}
			continue
		}
		deleted++
	}
	if failed > 0 {
		return deleted, fmt.Errorf("session cleanup failed for %d files", failed)
	}
	return deleted, nil
}

func (s *FileStorage) GetSession(ctx context.Context, tokenHash string) (*domain.Session, error) {
	if err := checkID("session", tokenHash); err != nil {
		return nil, err
	}
	var session domain.Session
	if err := readJSON(ctx, filepath.Join(s.root, "sessions", tokenHash+".json"), &session); err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *FileStorage) SaveSession(ctx context.Context, session *domain.Session) error {
	if session == nil {
		return errors.New("session is required")
	}
	if err := checkID("session", session.TokenHash); err != nil {
		return err
	}
	if session.Version == 0 {
		session.Version = domain.Version
	}
	return atomicJSON(ctx, filepath.Join(s.root, "sessions", session.TokenHash+".json"), session)
}

func (s *FileStorage) DeleteSession(ctx context.Context, tokenHash string) error {
	if err := checkID("session", tokenHash); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(s.root, "sessions", tokenHash+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *FileStorage) GetChannel(ctx context.Context, id string) (*domain.Channel, error) {
	if err := checkID("channel", id); err != nil {
		return nil, err
	}
	var channel domain.Channel
	if err := readJSON(ctx, filepath.Join(s.root, "channels", id, "meta.json"), &channel); err != nil {
		return nil, err
	}
	return &channel, nil
}

func (s *FileStorage) SaveChannel(ctx context.Context, channel *domain.Channel) error {
	if channel == nil {
		return errors.New("channel is required")
	}
	if err := checkID("channel", channel.ID); err != nil {
		return err
	}
	if channel.Version == 0 {
		channel.Version = domain.Version
	}
	return atomicJSON(ctx, filepath.Join(s.root, "channels", channel.ID, "meta.json"), channel)
}

func (s *FileStorage) ListChannels(ctx context.Context) ([]domain.Channel, error) {
	paths, err := filepath.Glob(filepath.Join(s.root, "channels", "*", "meta.json"))
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	sort.Strings(paths)
	channels := make([]domain.Channel, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var channel domain.Channel
		if err := readJSON(ctx, path, &channel); err != nil {
			return nil, err
		}
		channels = append(channels, channel)
	}
	return channels, nil
}

type userState struct {
	Threads  map[string]map[int64]channelState `json:"threads,omitempty"`
	Version  int                               `json:"version"`
	Channels map[string]channelState           `json:"channels"`
	Mentions map[string]time.Time              `json:"mentions,omitempty"`
}
type channelState struct {
	LastReadSeq int64 `json:"lastReadSeq"`
}

func (s *FileStorage) GetReadState(ctx context.Context, userID, channelID string) (int64, error) {
	if err := checkID("user", userID); err != nil {
		return 0, err
	}
	if err := checkID("channel", channelID); err != nil {
		return 0, err
	}
	var state userState
	err := readJSON(ctx, filepath.Join(s.root, "state", userID+".json"), &state)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return state.Channels[channelID].LastReadSeq, nil
}

func (s *FileStorage) GetReadStates(ctx context.Context, userID string) (map[string]int64, error) {
	if err := checkID("user", userID); err != nil {
		return nil, err
	}
	var state userState
	err := readJSON(ctx, filepath.Join(s.root, "state", userID+".json"), &state)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]int64{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(state.Channels))
	for channelID, channel := range state.Channels {
		out[channelID] = channel.LastReadSeq
	}
	return out, nil
}

func (s *FileStorage) SetReadState(ctx context.Context, userID, channelID string, seq int64) error {
	if err := checkID("user", userID); err != nil {
		return err
	}
	if err := checkID("channel", channelID); err != nil {
		return err
	}
	if seq < 0 {
		return errors.New("read sequence must not be negative")
	}
	lock := s.keyedLock(s.readLock, userID)
	lock.Lock()
	defer lock.Unlock()
	path := filepath.Join(s.root, "state", userID+".json")
	state := userState{Version: domain.Version, Channels: make(map[string]channelState)}
	if err := readJSON(ctx, path, &state); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if state.Channels == nil {
		state.Channels = make(map[string]channelState)
	}
	if seq <= state.Channels[channelID].LastReadSeq {
		return nil
	}
	state.Channels[channelID] = channelState{LastReadSeq: seq}
	return atomicJSON(ctx, path, &state)
}

func (s *FileStorage) ListMentions(ctx context.Context, userID string) ([]domain.Mention, error) {
	if err := checkID("user", userID); err != nil {
		return nil, err
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	var state userState
	err = readJSON(ctx, filepath.Join(s.root, "state", userID+".json"), &state)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	channels, err := s.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	for _, channel := range channels {
		lock := s.keyedLock(s.writers, channel.ID)
		lock.Lock()
		_, indexErr := s.positionsLocked(ctx, channel.ID)
		lock.Unlock()
		if indexErr != nil {
			return nil, indexErr
		}
	}
	s.mentionMu.Lock()
	refs := make([]string, 0, len(s.mentionUserRefs[userID])+len(s.legacyUserRefs[userID]))
	refs = append(refs, s.mentionUserRefs[userID]...)
	refs = append(refs, s.legacyUserRefs[userID]...)
	for _, groupID := range user.Groups {
		refs = append(refs, s.legacyGroupRefs[groupID]...)
	}
	indexed := make([]domain.Mention, 0, len(refs))
	seen := make(map[string]bool, len(refs))
	for _, messageID := range refs {
		if seen[messageID] {
			continue
		}
		seen[messageID] = true
		if mention, ok := s.mentionRecords[messageID]; ok {
			indexed = append(indexed, mention)
		}
	}
	s.mentionMu.Unlock()
	out := make([]domain.Mention, 0)
	for _, mention := range indexed {
		msg := mention.Message
		if t, ok := state.Mentions[msg.ID]; ok {
			copy := t
			mention.ReadAt = &copy
		}
		out = append(out, mention)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Message.Timestamp.After(out[j].Message.Timestamp) })
	return out, nil
}

func (s *FileStorage) indexMentionLocked(mention domain.Mention) {
	messageID := mention.Message.ID
	if _, exists := s.mentionRecords[messageID]; exists {
		return
	}
	if len(mention.Message.MentionUserIDs) > 0 {
		s.mentionRecords[messageID] = mention
		seen := make(map[string]bool, len(mention.Message.MentionUserIDs))
		for _, userID := range mention.Message.MentionUserIDs {
			if seen[userID] {
				continue
			}
			seen[userID] = true
			s.mentionUserRefs[userID] = append(s.mentionUserRefs[userID], messageID)
		}
		return
	}
	if mention.Message.MentionUserIDs != nil {
		return
	}
	matches := legacyMentionPattern.FindAllStringSubmatch(mention.Message.Text, -1)
	if len(matches) == 0 {
		return
	}
	s.mentionRecords[messageID] = mention
	seenUsers, seenGroups := map[string]bool{}, map[string]bool{}
	for _, match := range matches {
		if match[1] == "ai" {
			continue
		}
		if match[1] == "group" {
			if !seenGroups[match[2]] {
				seenGroups[match[2]] = true
				s.legacyGroupRefs[match[2]] = append(s.legacyGroupRefs[match[2]], messageID)
			}
		} else if !seenUsers[match[2]] {
			seenUsers[match[2]] = true
			s.legacyUserRefs[match[2]] = append(s.legacyUserRefs[match[2]], messageID)
		}
	}
}

var legacyMentionPattern = regexp.MustCompile(`(?:^|[[:space:]])@(?:(group|ai):)?([A-Za-z0-9][A-Za-z0-9_-]{0,63})`)

func (s *FileStorage) SetMentionRead(ctx context.Context, userID, messageID string, at time.Time) error {
	if err := checkID("user", userID); err != nil {
		return err
	}
	if !validID.MatchString(messageID) {
		return errors.New("invalid message id")
	}
	lock := s.keyedLock(s.readLock, userID)
	lock.Lock()
	defer lock.Unlock()
	path := filepath.Join(s.root, "state", userID+".json")
	state := userState{Version: domain.Version, Channels: make(map[string]channelState), Mentions: make(map[string]time.Time)}
	if err := readJSON(ctx, path, &state); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if state.Channels == nil {
		state.Channels = make(map[string]channelState)
	}
	if state.Mentions == nil {
		state.Mentions = make(map[string]time.Time)
	}
	if _, exists := state.Mentions[messageID]; exists {
		return nil
	}
	state.Mentions[messageID] = at
	return atomicJSON(ctx, path, &state)
}

func (s *FileStorage) GetSchedule(ctx context.Context, scheduleID string) (*domain.Schedule, error) {
	if err := checkID("schedule", scheduleID); err != nil {
		return nil, err
	}
	var schedule domain.Schedule
	if err := readJSON(ctx, filepath.Join(s.root, "schedules", scheduleID, "meta.json"), &schedule); err != nil {
		return nil, err
	}
	return &schedule, nil
}

func (s *FileStorage) ListSchedules(ctx context.Context) ([]domain.Schedule, error) {
	paths, err := filepath.Glob(filepath.Join(s.root, "schedules", "*", "meta.json"))
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	sort.Strings(paths)
	result := make([]domain.Schedule, 0, len(paths))
	for _, path := range paths {
		var schedule domain.Schedule
		if err := readJSON(ctx, path, &schedule); err != nil {
			// A damaged schedule must not prevent unrelated schedules loading.
			continue
		}
		result = append(result, schedule)
	}
	return result, nil
}

func (s *FileStorage) SaveSchedule(ctx context.Context, schedule *domain.Schedule) error {
	if schedule == nil {
		return errors.New("schedule is required")
	}
	if err := checkID("schedule", schedule.ID); err != nil {
		return err
	}
	if err := checkID("channel", schedule.ChannelID); err != nil {
		return err
	}
	if schedule.Version == 0 {
		schedule.Version = domain.Version
	}
	lock := s.keyedLock(s.scheduleWriters, schedule.ID)
	lock.Lock()
	defer lock.Unlock()
	return atomicJSON(ctx, filepath.Join(s.root, "schedules", schedule.ID, "meta.json"), schedule)
}

func (s *FileStorage) AddScheduleResponse(ctx context.Context, scheduleID string, response domain.ScheduleResponse) (domain.ScheduleResponse, error) {
	if err := checkID("schedule", scheduleID); err != nil {
		return domain.ScheduleResponse{}, err
	}
	if err := checkID("user", response.UserID); err != nil {
		return domain.ScheduleResponse{}, err
	}
	lock := s.keyedLock(s.scheduleWriters, scheduleID)
	lock.Lock()
	defer lock.Unlock()
	path := filepath.Join(s.root, "schedules", scheduleID, "responses.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return domain.ScheduleResponse{}, err
	}
	if err := truncateIncompleteTail(path); err != nil {
		return domain.ScheduleResponse{}, err
	}
	responses, err := readScheduleResponseLog(path)
	if errors.Is(err, os.ErrNotExist) {
		responses = nil
	} else if err != nil {
		return domain.ScheduleResponse{}, err
	}
	if len(responses) > 0 {
		response.Seq = responses[len(responses)-1].Seq + 1
	} else {
		response.Seq = 1
	}
	response.Version = domain.Version
	if response.UpdatedAt.IsZero() {
		response.UpdatedAt = time.Now()
	}
	b, err := json.Marshal(response)
	if err != nil {
		return domain.ScheduleResponse{}, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return domain.ScheduleResponse{}, err
	}
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return domain.ScheduleResponse{}, fmt.Errorf("commit schedule response: %w", err)
	}
	return response, nil
}

func (s *FileStorage) ListScheduleResponses(ctx context.Context, scheduleID string) ([]domain.ScheduleResponse, error) {
	if err := checkID("schedule", scheduleID); err != nil {
		return nil, err
	}
	path := filepath.Join(s.root, "schedules", scheduleID, "responses.jsonl")
	lock := s.keyedLock(s.scheduleWriters, scheduleID)
	lock.Lock()
	defer lock.Unlock()
	if err := truncateIncompleteTail(path); err != nil {
		return nil, err
	}
	events, err := readScheduleResponseLog(path)
	if errors.Is(err, os.ErrNotExist) {
		return []domain.ScheduleResponse{}, nil
	}
	if err != nil {
		return nil, err
	}
	latest := make(map[string]domain.ScheduleResponse)
	for _, event := range events {
		latest[event.UserID] = event
	}
	result := make([]domain.ScheduleResponse, 0, len(latest))
	for _, response := range latest {
		result = append(result, response)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UserID < result[j].UserID })
	return result, nil
}

func readScheduleResponseLog(path string) ([]domain.ScheduleResponse, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	result := []domain.ScheduleResponse{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event domain.ScheduleResponse
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("decode schedule response log %s: %w", path, err)
		}
		result = append(result, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func readJSON(ctx context.Context, path string, dst any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func atomicJSON(ctx context.Context, path string, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(append(b, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write temporary JSON: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace JSON %s: %w", path, err)
	}
	return nil
}
