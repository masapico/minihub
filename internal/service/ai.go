package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/masapico/minihub/internal/config"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
)

const aiMaxContextBytes = 1024 * 1024
const aiMaxContextMessages = 1000

type AIAccountSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type aiEndpoint struct {
	config.AIAccount
	token string
}

type aiJob struct {
	account             aiEndpoint
	author, channel, id string
	trigger             domain.Message
}

type aiDispatcher struct {
	ctx      context.Context
	cancel   context.CancelFunc
	accounts []aiEndpoint
	queue    chan aiJob
	wg       sync.WaitGroup
	logger   *slog.Logger
	client   *http.Client
}

// StartAI is called once at startup, before serving requests or modifying users.
func (s *Service) StartAI(ctx context.Context, accounts []config.AIAccount, logger *slog.Logger) error {
	if s.ai != nil {
		return errors.New("AI dispatcher already started")
	}
	if err := config.ValidateAIAccounts(accounts); err != nil {
		return err
	}
	active := []aiEndpoint{}
	for _, a := range accounts {
		if a.IsEnabled() {
			token := a.Token
			if a.TokenEnv != "" {
				token = os.Getenv(a.TokenEnv)
			}
			active = append(active, aiEndpoint{a, token})
		}
	}
	if len(active) == 0 {
		return nil
	}
	users, err := s.storage.ListUsers(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if strings.HasPrefix(u.ID, "ai_") {
			return fmt.Errorf("user %s conflicts with reserved AI identity prefix ai_", u.ID)
		}
	}
	if logger == nil {
		logger = slog.Default()
	}
	workerCtx, cancel := context.WithCancel(ctx)
	d := &aiDispatcher{ctx: workerCtx, cancel: cancel, accounts: active, queue: make(chan aiJob, 32), logger: logger,
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	s.ai = d
	for i := 0; i < 4; i++ {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			for {
				select {
				case <-d.ctx.Done():
					return
				case job := <-d.queue:
					if d.ctx.Err() != nil {
						return
					}
					s.runAI(d, job)
				}
			}
		}()
	}
	return nil
}

// CloseAI cancels outstanding requests and waits before Storage can be closed.
func (s *Service) CloseAI() {
	if s.ai != nil {
		s.ai.cancel()
		s.ai.wg.Wait()
		s.ai.client.CloseIdleConnections()
	}
}

func (s *Service) AIAccounts(ctx context.Context, userID string) ([]AIAccountSummary, error) {
	if _, err := s.user(ctx, userID); err != nil {
		return nil, err
	}
	result := []AIAccountSummary{}
	if s.ai != nil {
		for _, a := range s.ai.accounts {
			result = append(result, AIAccountSummary{a.ID, a.Name})
		}
	}
	return result, nil
}

func (s *Service) enqueueAI(author, channel string, m domain.Message) {
	d := s.ai
	if d == nil || m.AI != nil || d.ctx.Err() != nil {
		return
	}
	wanted := map[string]bool{}
	for _, match := range mentionPattern.FindAllStringSubmatch(m.Text, -1) {
		if match[1] == "ai" {
			wanted[match[2]] = true
		}
	}
	for _, a := range d.accounts {
		if !wanted[a.ID] {
			continue
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			d.logger.Error("create AI request identifier", "channelId", channel)
			continue
		}
		job := aiJob{account: a, author: author, channel: channel, id: hex.EncodeToString(id[:]), trigger: m}
		select {
		case <-d.ctx.Done():
			return
		case d.queue <- job:
		default:
			s.aiFailure(d, job, "現在AIへの依頼が混み合っています。時間をおいて再度メンションしてください。", "queue_full")
		}
	}
}

func (j aiJob) root() int64 {
	if j.trigger.ThreadRootSeq > 0 {
		return j.trigger.ThreadRootSeq
	}
	return j.trigger.Seq
}

func (s *Service) validateAIJob(ctx context.Context, j aiJob) error {
	if err := s.canPost(ctx, j.author, j.channel); err != nil {
		return err
	}
	for _, seq := range []int64{j.root(), j.trigger.Seq} {
		m, err := s.storage.GetMessage(ctx, j.channel, seq)
		if err != nil {
			return err
		}
		m, err = s.publicMessage(ctx, j.channel, m)
		if err != nil {
			return err
		}
		if m.WithdrawnAt != nil || (seq == j.trigger.Seq && m.ID != j.trigger.ID) {
			return ErrForbidden
		}
	}
	return nil
}

type aiAuthor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}
type aiContextMessage struct {
	ID        string    `json:"id"`
	Seq       int64     `json:"seq"`
	Timestamp time.Time `json:"ts"`
	Author    aiAuthor  `json:"author"`
	Text      string    `json:"text"`
}
type aiRequest struct {
	Version   int              `json:"version"`
	RequestID string           `json:"requestId"`
	AIAccount AIAccountSummary `json:"aiAccount"`
	Channel   struct {
		ID   string             `json:"id"`
		Name string             `json:"name"`
		Type domain.ChannelType `json:"type"`
	} `json:"channel"`
	RequestedBy AIAccountSummary `json:"requestedBy"`
	Trigger     struct {
		ID            string `json:"id"`
		Seq           int64  `json:"seq"`
		ThreadRootSeq int64  `json:"threadRootSeq"`
	} `json:"trigger"`
	Messages []aiContextMessage `json:"messages"`
}

var errAIContextLimit = errors.New("AI context limit exceeded")

func (s *Service) aiPayload(ctx context.Context, j aiJob) ([]byte, error) {
	if err := s.validateAIJob(ctx, j); err != nil {
		return nil, err
	}
	ch, err := s.channel(ctx, j.channel)
	if err != nil {
		return nil, err
	}
	users, err := s.storage.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, u := range users {
		names[u.ID] = u.Name
	}
	p := aiRequest{Version: 1, RequestID: j.id, AIAccount: AIAccountSummary{j.account.ID, j.account.Name}, RequestedBy: AIAccountSummary{j.author, names[j.author]}, Messages: []aiContextMessage{}}
	p.Channel.ID, p.Channel.Name, p.Channel.Type = ch.ID, ch.Name, ch.Type
	p.Trigger.ID, p.Trigger.Seq, p.Trigger.ThreadRootSeq = j.trigger.ID, j.trigger.Seq, j.trigger.ThreadRootSeq
	total := 0
	count := 0
	add := func(m domain.Message) error {
		count++
		if count > aiMaxContextMessages {
			return errAIContextLimit
		}
		m, err = s.publicMessage(ctx, j.channel, m)
		if err != nil {
			return err
		}
		if m.WithdrawnAt != nil {
			return nil
		}
		author := aiAuthor{Kind: "user", ID: m.UserID, Name: names[m.UserID]}
		if author.Name == "" {
			author.Name = m.UserID
		}
		if m.AI != nil {
			author = aiAuthor{Kind: "ai", ID: m.AI.ID, Name: m.AI.Name}
		}
		item := aiContextMessage{m.ID, m.Seq, m.Timestamp, author, m.Text}
		encoded, err := json.Marshal(item)
		if err != nil {
			return err
		}
		total += len(encoded)
		if total > aiMaxContextBytes {
			return errAIContextLimit
		}
		p.Messages = append(p.Messages, item)
		return nil
	}
	root, err := s.storage.GetMessage(ctx, j.channel, j.root())
	if err != nil {
		return nil, err
	}
	if err := add(root); err != nil {
		return nil, err
	}
	if j.trigger.ThreadRootSeq > 0 {
		after := int64(0)
		for {
			page, err := s.storage.GetMessages(ctx, j.channel, storage.MessageQuery{ThreadRootSeq: j.root(), AfterSeq: &after, Limit: 100})
			if err != nil {
				return nil, err
			}
			done := false
			for _, m := range page.Messages {
				if m.Seq > j.trigger.Seq {
					done = true
					break
				}
				if err := add(m); err != nil {
					return nil, err
				}
				after = m.Seq
				if m.Seq == j.trigger.Seq {
					done = true
					break
				}
			}
			if done || page.NextAfter == nil || len(page.Messages) == 0 {
				break
			}
		}
	}
	b, err := json.Marshal(p)
	if len(b) > aiMaxContextBytes {
		return nil, errAIContextLimit
	}
	return b, err
}

func (s *Service) runAI(d *aiDispatcher, j aiJob) {
	ctx, cancel := context.WithTimeout(d.ctx, j.account.RequestTimeout())
	defer cancel()
	body, err := s.aiPayload(ctx, j)
	if err != nil {
		if errors.Is(err, errAIContextLimit) {
			s.aiFailure(d, j, "スレッドが送信上限（1,000件・1 MiB）を超えています。短い新規投稿から呼び出してください。", "context_limit")
		} else {
			s.aiFailure(d, j, "AIへの依頼を準備できませんでした。", "context_unavailable")
		}
		return
	}
	if err := s.validateAIJob(ctx, j); err != nil {
		s.aiFailure(d, j, "AIへの依頼を送信できませんでした。時間をおいて再度お試しください。", "request_unavailable")
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.account.URL, bytes.NewReader(body))
	if err != nil {
		s.aiFailure(d, j, "AIへの接続に失敗しました。", "request_invalid")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-MiniHub-Request-ID", j.id)
	if j.account.token != "" {
		req.Header.Set("Authorization", "Bearer "+j.account.token)
	}
	// No automatic retry: this POST can trigger external business operations.
	resp, err := d.client.Do(req)
	if err != nil {
		s.aiFailure(d, j, "AIへの接続に失敗したか、処理がタイムアウトしました。再送すると処理が重複する可能性があるため、実行状況を確認してください。", "request_failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return
	}
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK || media != "application/json" {
		s.aiFailure(d, j, "AIから正常な応答を受信できませんでした。", "response_invalid")
		return
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, aiMaxContextBytes+1))
	var reply struct {
		Text *string `json:"text"`
	}
	if err != nil || len(b) > aiMaxContextBytes || json.Unmarshal(b, &reply) != nil || reply.Text == nil || len([]byte(*reply.Text)) > MaxMessageBytes {
		s.aiFailure(d, j, "AIの回答形式または回答サイズが不正です（本文は16 KiB以内）。", "response_invalid")
		return
	}
	if strings.TrimSpace(*reply.Text) == "" {
		return
	}
	if err := s.saveAIReply(d.ctx, j, *reply.Text, "answer"); err != nil {
		d.logger.Warn("AI reply not saved", "requestId", j.id, "channelId", j.channel)
	}
}

func (s *Service) aiFailure(d *aiDispatcher, j aiJob, text, code string) {
	d.logger.Warn("AI request failed", "requestId", j.id, "aiId", j.account.ID, "channelId", j.channel, "code", code)
	if d.ctx.Err() == nil {
		if err := s.saveAIReply(d.ctx, j, text, "error"); err != nil {
			d.logger.Warn("AI failure notice not saved", "requestId", j.id, "channelId", j.channel)
		}
	}
}

func (s *Service) saveAIReply(ctx context.Context, j aiJob, text, kind string) error {
	rootLock := s.channelLock("withdraw:message:" + j.channel + ":" + strconv.FormatInt(j.root(), 10))
	rootLock.Lock()
	defer rootLock.Unlock()
	if j.trigger.Seq != j.root() {
		triggerLock := s.channelLock("withdraw:message:" + j.channel + ":" + strconv.FormatInt(j.trigger.Seq, 10))
		triggerLock.Lock()
		defer triggerLock.Unlock()
	}
	if err := s.validateAIJob(ctx, j); err != nil {
		return err
	}
	m, err := s.storage.AddMessage(ctx, j.channel, domain.Message{UserID: "ai_" + j.account.ID, Text: text, ThreadRootSeq: j.root(), MentionUserIDs: []string{}, AI: &domain.AIMessage{ID: j.account.ID, Name: j.account.Name, RequestID: j.id, TriggerMessageID: j.trigger.ID, RequestedBy: j.author, Kind: kind}})
	if err != nil {
		return err
	}
	if publisher, ok := s.publisher.(ThreadPublisher); ok {
		publisher.ThreadUpdated(ctx, j.channel, j.root(), m.Seq, m.UserID)
	}
	return nil
}
