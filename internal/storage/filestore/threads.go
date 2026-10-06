package filestore

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
)

// All access to an index is serialized by the channel writer. Bodies stay on disk.
type messagePosition struct {
	path      string
	offset    int64
	length    int
	seq, root int64
	author    string
}
type threadIndex struct {
	replies []messagePosition
	authors map[string][]int64
	updated time.Time
	latest  int64
}
type positionIndex struct {
	latest  int64
	main    []messagePosition
	bySeq   map[int64]messagePosition
	threads map[int64]*threadIndex
}

func (idx *positionIndex) add(m domain.Message, pos messagePosition) {
	idx.bySeq[m.Seq] = pos
	if m.Seq > idx.latest {
		idx.latest = m.Seq
	}
	if m.ThreadRootSeq == 0 {
		idx.main = append(idx.main, pos)
		return
	}
	t := idx.threads[m.ThreadRootSeq]
	if t == nil {
		t = &threadIndex{authors: make(map[string][]int64)}
		idx.threads[m.ThreadRootSeq] = t
	}
	t.replies = append(t.replies, pos)
	t.authors[m.UserID] = append(t.authors[m.UserID], m.Seq)
	if m.Seq > t.latest {
		t.updated = m.Timestamp
		t.latest = m.Seq
	}
}
func (s *FileStorage) positionsLocked(ctx context.Context, channelID string) (*positionIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	idx := s.positionIndexes[channelID]
	s.mu.Unlock()
	if idx != nil {
		return idx, nil
	}
	idx = &positionIndex{bySeq: make(map[int64]messagePosition), threads: make(map[int64]*threadIndex)}
	files, err := s.messageFiles(channelID)
	if err != nil {
		return nil, err
	}
	// Files can be dated out of sequence (clock changes); sort the compact positions afterwards.
	mentions := []domain.Mention{}
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		reader := bufio.NewReader(f)
		var offset int64
		for {
			if err := ctx.Err(); err != nil {
				_ = f.Close()
				return nil, err
			}
			line, readErr := reader.ReadBytes('\n')
			if errors.Is(readErr, io.EOF) {
				break
			} // Never index an incomplete final line.
			if readErr != nil {
				_ = f.Close()
				return nil, readErr
			}
			var m domain.Message
			if err := json.Unmarshal(line, &m); err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("corrupt message log %s offset %d: %w", path, offset, err)
			}
			if _, duplicate := idx.bySeq[m.Seq]; duplicate || m.Seq < 1 || m.ThreadRootSeq < 0 || m.ThreadRootSeq >= m.Seq {
				_ = f.Close()
				return nil, fmt.Errorf("corrupt message sequence in %s at %d", path, offset)
			}
			idx.add(m, messagePosition{path: path, offset: offset, length: len(line), seq: m.Seq, root: m.ThreadRootSeq, author: m.UserID})
			if len(m.MentionUserIDs) > 0 || m.MentionUserIDs == nil && legacyMentionPattern.MatchString(m.Text) {
				mentions = append(mentions, domain.Mention{Message: m, ChannelID: channelID})
			}
			offset += int64(len(line))
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	var watermark struct {
		LastSeq int64 `json:"lastSeq"`
	}
	if err := readJSON(ctx, filepath.Join(s.root, "channels", channelID, "sequence.json"), &watermark); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if watermark.LastSeq > idx.latest {
		idx.latest = watermark.LastSeq
	}
	sort.Slice(idx.main, func(i, j int) bool { return idx.main[i].seq < idx.main[j].seq })
	for root, t := range idx.threads {
		parent, ok := idx.bySeq[root]
		if !ok || parent.root != 0 {
			return nil, fmt.Errorf("missing thread root in channel %s: %d", channelID, root)
		}
		sort.Slice(t.replies, func(i, j int) bool { return t.replies[i].seq < t.replies[j].seq })
		for _, seqs := range t.authors {
			sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
		}

	}
	s.mentionMu.Lock()
	for _, mention := range mentions {
		s.indexMentionLocked(mention)
	}
	s.mentionMu.Unlock()
	s.mu.Lock()
	s.positionIndexes[channelID] = idx
	s.mu.Unlock()
	return idx, nil
}
func readPosition(pos messagePosition) (domain.Message, error) {
	f, err := os.Open(pos.path)
	if err != nil {
		return domain.Message{}, err
	}
	b := make([]byte, pos.length)
	_, err = f.ReadAt(b, pos.offset)
	closeErr := f.Close()
	if err != nil {
		return domain.Message{}, err
	}
	if closeErr != nil {
		return domain.Message{}, closeErr
	}
	var m domain.Message
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	if m.Seq != pos.seq {
		return m, errors.New("message index does not match log")
	}
	return m, nil
}
func (s *FileStorage) GetMessage(ctx context.Context, channelID string, seq int64) (domain.Message, error) {
	if err := checkID("channel", channelID); err != nil {
		return domain.Message{}, err
	}
	lock := s.keyedLock(s.writers, channelID)
	lock.Lock()
	defer lock.Unlock()
	idx, err := s.positionsLocked(ctx, channelID)
	if err != nil {
		return domain.Message{}, err
	}
	pos, ok := idx.bySeq[seq]
	if !ok {
		return domain.Message{}, os.ErrNotExist
	}
	return readPosition(pos)
}
func (s *FileStorage) positionPage(ctx context.Context, idx *positionIndex, q storage.MessageQuery) (storage.MessagePage, error) {
	page := storage.MessagePage{Messages: []domain.Message{}}
	cursors := 0
	if q.BeforeSeq != nil {
		cursors++
	}
	if q.AfterSeq != nil {
		cursors++
	}
	if q.AroundSeq != nil {
		cursors++
	}
	if q.Limit < 1 || cursors > 1 || q.BeforeSeq != nil && *q.BeforeSeq < 1 || q.AfterSeq != nil && *q.AfterSeq < 0 || q.AroundSeq != nil && *q.AroundSeq < 1 {
		return page, errors.New("invalid message query")
	}
	positions := idx.main
	if q.ThreadRootSeq > 0 {
		p, ok := idx.bySeq[q.ThreadRootSeq]
		if !ok || p.root != 0 {
			return page, os.ErrNotExist
		}
		positions = nil
		if t := idx.threads[q.ThreadRootSeq]; t != nil {
			positions = t.replies
		}
	}
	n := len(positions)
	start, end := 0, n
	if q.AfterSeq != nil {
		start = sort.Search(n, func(i int) bool { return positions[i].seq > *q.AfterSeq })
		end = min(n, start+q.Limit)
	} else if q.AroundSeq != nil {
		i := sort.Search(n, func(i int) bool { return positions[i].seq >= *q.AroundSeq })
		start = max(0, i-q.Limit/2)
		end = min(n, start+q.Limit)
	} else {
		if q.BeforeSeq != nil {
			end = sort.Search(n, func(i int) bool { return positions[i].seq >= *q.BeforeSeq })
		}
		start = max(0, end-q.Limit)
	}
	// Keep a file open for the whole page instead of opening once for every row.
	handles := map[string]*os.File{}
	defer func() {
		for _, f := range handles {
			_ = f.Close()
		}
	}()
	for _, pos := range positions[start:end] {
		if err := ctx.Err(); err != nil {
			return page, err
		}
		f := handles[pos.path]
		if f == nil {
			var err error
			f, err = os.Open(pos.path)
			if err != nil {
				return page, err
			}
			handles[pos.path] = f
		}
		b := make([]byte, pos.length)
		if _, err := f.ReadAt(b, pos.offset); err != nil {
			return page, err
		}
		var m domain.Message
		if err := json.Unmarshal(b, &m); err != nil {
			return page, err
		}
		if m.Seq != pos.seq {
			return page, errors.New("message index does not match log")
		}
		page.Messages = append(page.Messages, m)
	}
	for path, f := range handles {
		err := f.Close()
		delete(handles, path)
		if err != nil {
			return page, err
		}
	}
	if len(page.Messages) > 0 {
		if start > 0 && (q.AfterSeq == nil || q.ThreadRootSeq > 0) {
			v := page.Messages[0].Seq
			page.NextBefore = &v
		}
		if end < n && (q.BeforeSeq == nil && q.AfterSeq != nil || q.AroundSeq != nil || q.ThreadRootSeq > 0) {
			v := page.Messages[len(page.Messages)-1].Seq
			page.NextAfter = &v
		}
	}
	return page, nil
}
func (s *FileStorage) MessageReadCounts(ctx context.Context, channelID string, read int64) (int64, int, error) {
	if err := checkID("channel", channelID); err != nil {
		return 0, 0, err
	}
	lock := s.keyedLock(s.writers, channelID)
	lock.Lock()
	defer lock.Unlock()
	idx, err := s.positionsLocked(ctx, channelID)
	if err != nil {
		return 0, 0, err
	}
	n := len(idx.main)
	if n == 0 {
		return 0, 0, nil
	}
	return idx.main[n-1].seq, n - sort.Search(n, func(i int) bool { return idx.main[i].seq > read }), nil
}
func (s *FileStorage) ThreadSummaries(ctx context.Context, userID, channelID string, filters ...storage.ThreadFilter) ([]storage.ThreadSummary, error) {
	if err := checkID("user", userID); err != nil {
		return nil, err
	}
	if err := checkID("channel", channelID); err != nil {
		return nil, err
	}
	var state userState
	if err := readJSON(ctx, filepath.Join(s.root, "state", userID+".json"), &state); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lock := s.keyedLock(s.writers, channelID)
	lock.Lock()
	defer lock.Unlock()
	idx, err := s.positionsLocked(ctx, channelID)
	if err != nil {
		return nil, err
	}
	filter := storage.ThreadFilter{}
	if len(filters) > 0 {
		filter = filters[0]
	}
	roots := filter.Roots
	selected := idx.threads
	if len(roots) > 0 {
		selected = make(map[int64]*threadIndex, len(roots))
		for _, root := range roots {
			if thread := idx.threads[root]; thread != nil {
				selected[root] = thread
			}
		}
	}
	capacity := len(selected)
	if filter.Participating {
		capacity = min(capacity, 32)
	}
	out := make([]storage.ThreadSummary, 0, capacity)
	for root, t := range selected {
		participating := idx.bySeq[root].author == userID || len(t.authors[userID]) > 0
		if filter.Participating && !participating {
			continue
		}
		read := state.Threads[channelID][root].LastReadSeq
		start := sort.Search(len(t.replies), func(i int) bool { return t.replies[i].seq > read })
		own := t.authors[userID]
		ownStart := sort.Search(len(own), func(i int) bool { return own[i] > read })
		unread := len(t.replies) - start - (len(own) - ownStart)
		var first int64
		if unread > 0 {
			for _, p := range t.replies[start:] {
				if p.author != userID {
					first = p.seq
					break
				}
			}
		}
		out = append(out, storage.ThreadSummary{ChannelID: channelID, RootSeq: root, ReplyCount: len(t.replies), LatestSeq: t.replies[len(t.replies)-1].seq, UpdatedAt: t.updated, LastReadSeq: read, UnreadCount: unread, FirstUnreadSeq: first, Participating: participating})
	}
	return out, nil
}
func (s *FileStorage) SetThreadReadState(ctx context.Context, userID, channelID string, root, seq int64) error {
	if err := checkID("user", userID); err != nil {
		return err
	}
	if err := checkID("channel", channelID); err != nil {
		return err
	}
	m, err := s.GetMessage(ctx, channelID, seq)
	if err != nil {
		return err
	}
	if m.ThreadRootSeq != root || root < 1 {
		return errors.New("invalid thread read position")
	}
	lock := s.keyedLock(s.readLock, userID)
	lock.Lock()
	defer lock.Unlock()
	path := filepath.Join(s.root, "state", userID+".json")
	state := userState{Version: domain.Version}
	if err := readJSON(ctx, path, &state); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if state.Threads == nil {
		state.Threads = map[string]map[int64]channelState{}
	}
	if state.Threads[channelID] == nil {
		state.Threads[channelID] = map[int64]channelState{}
	}
	if state.Threads[channelID][root].LastReadSeq >= seq {
		return nil
	}
	state.Threads[channelID][root] = channelState{LastReadSeq: seq}
	return atomicJSON(ctx, path, &state)
}
