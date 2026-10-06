package storage

import (
	"context"
	"time"

	"github.com/masapico/minihub/internal/domain"
)

type MessageQuery struct {
	ThreadRootSeq int64
	BeforeSeq     *int64
	AfterSeq      *int64
	AroundSeq     *int64
	Limit         int
}

type MentionPage struct {
	Mentions    []domain.Mention `json:"mentions"`
	NextCursor  string           `json:"nextCursor,omitempty"`
	UnreadCount int              `json:"unreadCount"`
}

type MessagePage struct {
	Threads    map[int64]ThreadSummary `json:"threads,omitempty"`
	Messages   []domain.Message        `json:"messages"`
	NextBefore *int64                  `json:"nextBefore"`
	NextAfter  *int64                  `json:"nextAfter"`
}

type ThreadFilter struct {
	Roots         []int64
	Participating bool
}

type ThreadSummary struct {
	ChannelID      string          `json:"channelId"`
	RootSeq        int64           `json:"rootSeq"`
	ReplyCount     int             `json:"replyCount"`
	LatestSeq      int64           `json:"latestSeq"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	LastReadSeq    int64           `json:"lastReadSeq"`
	UnreadCount    int             `json:"unreadCount"`
	FirstUnreadSeq int64           `json:"firstUnreadSeq,omitempty"`
	Participating  bool            `json:"participating"`
	Root           *domain.Message `json:"root,omitempty"`
}

type Storage interface {
	GetWithdrawal(context.Context, string, string, string) (*domain.Withdrawal, error)
	SaveWithdrawal(context.Context, domain.Withdrawal) error
	RestoreWithdrawal(context.Context, string, string, string, string, time.Time) error
	GetPoll(context.Context, string) (*domain.Poll, error)
	ListPolls(context.Context) ([]domain.Poll, error)
	SavePoll(context.Context, *domain.Poll) error
	AddPollResponse(context.Context, string, domain.PollResponse) (domain.PollResponse, error)
	ListPollResponses(context.Context, string) ([]domain.PollResponse, error)
	GetMessage(context.Context, string, int64) (domain.Message, error)
	MessageReadCounts(context.Context, string, int64) (int64, int, error)
	ThreadSummaries(context.Context, string, string, ...ThreadFilter) ([]ThreadSummary, error)
	SetThreadReadState(context.Context, string, string, int64, int64) error
	AddMessage(context.Context, string, domain.Message) (domain.Message, error)
	GetMessages(context.Context, string, MessageQuery) (MessagePage, error)
	LatestMessageSeq(context.Context, string) (int64, error)
	SetReaction(context.Context, string, domain.ReactionEvent) (bool, error)
	GetReactions(context.Context, string, int64) ([]domain.ReactionState, error)
	GetReactionsForMessages(context.Context, string, []int64) ([]domain.ReactionState, error)
	GetReactionUsers(context.Context, string, int64, string) ([]string, error)
	GetUser(context.Context, string) (*domain.User, error)
	ListUsers(context.Context) ([]domain.User, error)
	SaveUser(context.Context, *domain.User) error
	GetGroup(context.Context, string) (*domain.Group, error)
	ListGroups(context.Context) ([]domain.Group, error)
	SaveGroup(context.Context, *domain.Group) error
	DeleteGroup(context.Context, string) error
	GetSession(context.Context, string) (*domain.Session, error)
	SaveSession(context.Context, *domain.Session) error
	DeleteSession(context.Context, string) error
	DeleteExpiredSessions(context.Context, time.Time) (int, error)
	GetChannel(context.Context, string) (*domain.Channel, error)
	ListChannels(context.Context) ([]domain.Channel, error)
	SaveChannel(context.Context, *domain.Channel) error
	GetReadState(context.Context, string, string) (int64, error)
	GetReadStates(context.Context, string) (map[string]int64, error)
	SetReadState(context.Context, string, string, int64) error
	ListMentions(context.Context, string) ([]domain.Mention, error)
	SetMentionRead(context.Context, string, string, time.Time) error
	GetSchedule(context.Context, string) (*domain.Schedule, error)
	ListSchedules(context.Context) ([]domain.Schedule, error)
	SaveSchedule(context.Context, *domain.Schedule) error
	AddScheduleResponse(context.Context, string, domain.ScheduleResponse) (domain.ScheduleResponse, error)
	ListScheduleResponses(context.Context, string) ([]domain.ScheduleResponse, error)
}
