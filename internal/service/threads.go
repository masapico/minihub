package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
)

type ThreadPublisher interface {
	ThreadUpdated(context.Context, string, int64, int64, string)
}
type ThreadPage struct {
	storage.MessagePage
	Root    domain.Message        `json:"root"`
	Summary storage.ThreadSummary `json:"summary"`
}
type ThreadList struct {
	Threads     []storage.ThreadSummary `json:"threads"`
	NextCursor  string                  `json:"nextCursor,omitempty"`
	UnreadCount int                     `json:"unreadCount"`
}

func (s *Service) threadRoot(ctx context.Context, userID, channelID string, root int64) (domain.Message, error) {
	if err := s.canRead(ctx, userID, channelID); err != nil {
		return domain.Message{}, err
	}
	if root < 1 {
		return domain.Message{}, fmt.Errorf("%w: スレッド番号には1以上の値を指定してください", ErrInvalid)
	}
	m, err := s.storage.GetMessage(ctx, channelID, root)
	if errors.Is(err, os.ErrNotExist) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	if m.ThreadRootSeq != 0 {
		return m, fmt.Errorf("%w: 返信をスレッドの起点にはできません", ErrInvalid)
	}
	return s.publicMessage(ctx, channelID, m)
}
func (s *Service) PostThreadMessage(ctx context.Context, userID, channelID string, root int64, text string, attachmentIDs ...string) (domain.Message, error) {
	var committed domain.Message
	lock := s.channelLock("withdraw:message:" + channelID + ":" + strconv.FormatInt(root, 10))
	lock.Lock()
	defer func() {
		lock.Unlock()
		if committed.ID != "" {
			s.enqueueAI(userID, channelID, committed)
		}
	}()
	if err := s.canPost(ctx, userID, channelID); err != nil {
		return domain.Message{}, err
	}
	parent, err := s.threadRoot(ctx, userID, channelID, root)
	if err != nil {
		return domain.Message{}, err
	}
	if parent.WithdrawnAt != nil {
		return domain.Message{}, ErrInvalid
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
	m, err := s.storage.AddMessage(ctx, channelID, domain.Message{UserID: userID, Text: text, ThreadRootSeq: root, MentionUserIDs: recipients, Attachments: attachments})
	if err != nil {
		return m, err
	}
	committed = m
	if publisher, ok := s.publisher.(ThreadPublisher); ok {
		publisher.ThreadUpdated(ctx, channelID, root, m.Seq, userID)
	}
	return m, nil
}
func validateThreadQuery(q *storage.MessageQuery) error {
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.Limit < 1 || q.Limit > 100 {
		return fmt.Errorf("%w: 取得件数は1以上100以下で指定してください", ErrInvalid)
	}
	n := 0
	if q.BeforeSeq != nil {
		n++
	}
	if q.AfterSeq != nil {
		n++
	}
	if q.AroundSeq != nil {
		n++
	}
	if n > 1 || q.BeforeSeq != nil && *q.BeforeSeq < 1 || q.AfterSeq != nil && *q.AfterSeq < 0 || q.AroundSeq != nil && *q.AroundSeq < 1 {
		return fmt.Errorf("%w: 履歴の取得位置が正しくありません", ErrInvalid)
	}
	return nil
}
func (s *Service) GetThreadMessages(ctx context.Context, userID, channelID string, root int64, q storage.MessageQuery) (ThreadPage, error) {
	parent, err := s.threadRoot(ctx, userID, channelID, root)
	if err != nil {
		return ThreadPage{}, err
	}
	if err := validateThreadQuery(&q); err != nil {
		return ThreadPage{}, err
	}
	summary := storage.ThreadSummary{ChannelID: channelID, RootSeq: root, Participating: parent.UserID == userID}
	summaries, err := s.storage.ThreadSummaries(ctx, userID, channelID, storage.ThreadFilter{Roots: []int64{root}})
	if err != nil {
		return ThreadPage{}, err
	}
	for _, t := range summaries {
		if t.RootSeq == root {
			summary = t
			break
		}
	}
	if q.BeforeSeq == nil && q.AfterSeq == nil && q.AroundSeq == nil && summary.FirstUnreadSeq > 0 {
		seq := summary.FirstUnreadSeq
		q.AroundSeq = &seq
	}
	q.ThreadRootSeq = root
	page, err := s.storage.GetMessages(ctx, channelID, q)
	if err != nil {
		return ThreadPage{}, err
	}
	for i := range page.Messages {
		page.Messages[i], err = s.publicMessage(ctx, channelID, page.Messages[i])
		if err != nil {
			return ThreadPage{}, err
		}
	}
	if summary.Root != nil {
		summary.Root = &parent
	}
	// A post may have committed between choosing the initial page and reading it.
	// Refresh the unread boundary so clients never skip unseen replies in that race.
	summaries, err = s.storage.ThreadSummaries(ctx, userID, channelID, storage.ThreadFilter{Roots: []int64{root}})
	if err != nil {
		return ThreadPage{}, err
	}
	for _, t := range summaries {
		if t.RootSeq == root {
			summary = t
			break
		}
	}
	return ThreadPage{MessagePage: page, Root: parent, Summary: summary}, nil
}
func (s *Service) MarkThreadRead(ctx context.Context, userID, channelID string, root, seq int64) error {
	if _, err := s.threadRoot(ctx, userID, channelID, root); err != nil {
		return err
	}
	if seq < 1 {
		return fmt.Errorf("%w: 既読位置が正しくありません", ErrInvalid)
	}
	m, err := s.storage.GetMessage(ctx, channelID, seq)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if m.ThreadRootSeq != root {
		return fmt.Errorf("%w: 指定位置はこのスレッドの返信ではありません", ErrInvalid)
	}
	return s.storage.SetThreadReadState(ctx, userID, channelID, root, seq)
}

type threadCursor struct {
	Updated time.Time `json:"t"`
	Channel string    `json:"c"`
	Root    int64     `json:"r"`
}

func threadLess(a, b storage.ThreadSummary) bool {
	if !a.UpdatedAt.Equal(b.UpdatedAt) {
		return a.UpdatedAt.After(b.UpdatedAt)
	}
	if a.ChannelID != b.ChannelID {
		return a.ChannelID < b.ChannelID
	}
	return a.RootSeq > b.RootSeq
}
func (s *Service) ListThreads(ctx context.Context, userID, channelID string, participating, unread bool, cursor string, limit int) (ThreadList, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return ThreadList{}, fmt.Errorf("%w: 取得件数は1以上100以下で指定してください", ErrInvalid)
	}
	var boundary *storage.ThreadSummary
	if cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		var c threadCursor
		if err != nil || json.Unmarshal(b, &c) != nil || c.Updated.IsZero() || c.Root < 1 || !validUserID.MatchString(c.Channel) {
			return ThreadList{}, fmt.Errorf("%w: カーソルが正しくありません", ErrInvalid)
		}
		boundary = &storage.ThreadSummary{UpdatedAt: c.Updated, ChannelID: c.Channel, RootSeq: c.Root}
	}
	var channels []domain.Channel
	if channelID != "" {
		if err := s.canRead(ctx, userID, channelID); err != nil {
			return ThreadList{}, err
		}
		channels = []domain.Channel{{ID: channelID}}
	} else {
		var err error
		channels, err = s.ListChannels(ctx, userID)
		if err != nil {
			return ThreadList{}, err
		}
	}
	candidates := []storage.ThreadSummary{}
	unreadCount := 0
	for _, c := range channels {
		summaries, err := s.storage.ThreadSummaries(ctx, userID, c.ID, storage.ThreadFilter{Participating: participating})
		if err != nil {
			return ThreadList{}, err
		}
		for _, t := range summaries {
			if participating && !t.Participating {
				continue
			}
			if t.UnreadCount > 0 {
				unreadCount++
			}
			if unread && t.UnreadCount == 0 {
				continue
			}
			if boundary == nil || threadLess(*boundary, t) {
				candidates = append(candidates, t)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return threadLess(candidates[i], candidates[j]) })
	page := ThreadList{Threads: []storage.ThreadSummary{}, UnreadCount: unreadCount}
	if len(candidates) > limit {
		last := candidates[limit-1]
		b, _ := json.Marshal(threadCursor{last.UpdatedAt, last.ChannelID, last.RootSeq})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(b)
		candidates = candidates[:limit]
	}
	for _, t := range candidates {
		root, err := s.storage.GetMessage(ctx, t.ChannelID, t.RootSeq)
		if err != nil {
			return ThreadList{}, err
		}
		root, err = s.publicMessage(ctx, t.ChannelID, root)
		if err != nil {
			return ThreadList{}, err
		}
		text := []rune(root.Text)
		if len(text) > 120 {
			root.Text = string(text[:120]) + "…"
		}
		t.Root = &root
		page.Threads = append(page.Threads, t)
	}
	return page, nil
}

func (s *Service) ThreadSummaries(ctx context.Context, userID, channelID string, roots map[int64]bool) (map[int64]storage.ThreadSummary, error) {
	if err := s.canRead(ctx, userID, channelID); err != nil {
		return nil, err
	}
	seqs := make([]int64, 0, len(roots))
	for root := range roots {
		seqs = append(seqs, root)
	}
	if len(seqs) == 0 {
		return map[int64]storage.ThreadSummary{}, nil
	}
	all, err := s.storage.ThreadSummaries(ctx, userID, channelID, storage.ThreadFilter{Roots: seqs})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]storage.ThreadSummary)
	for _, t := range all {
		if roots[t.RootSeq] {
			out[t.RootSeq] = t
		}
	}
	return out, nil
}
