package domain

import "time"

const Version = 1

type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

type User struct {
	Version        int      `json:"version"`
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Groups         []string `json:"groups,omitempty"`
	Role           Role     `json:"role"`
	Enabled        bool     `json:"enabled"`
	PasswordHash   string   `json:"passwordHash,omitempty"`
	AuthGeneration uint64   `json:"authGeneration,omitempty"`
}

// Group is organization metadata. User.Groups is the source of truth for membership.
type Group struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Name    string `json:"name"`
}

// Session contains only the hash of the browser token; the raw token is never persisted.
type Session struct {
	Version        int       `json:"version"`
	TokenHash      string    `json:"tokenHash"`
	UserID         string    `json:"userId"`
	CSRFToken      string    `json:"csrfToken"`
	ExpiresAt      time.Time `json:"expiresAt"`
	AuthGeneration uint64    `json:"authGeneration,omitempty"`
}

type ChannelType string

const (
	ChannelPublic  ChannelType = "public"
	ChannelPrivate ChannelType = "private"
)

type Channel struct {
	Version    int         `json:"version"`
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Type       ChannelType `json:"type"`
	CreatedBy  string      `json:"createdBy"`
	CreatedAt  time.Time   `json:"createdAt"`
	Members    []string    `json:"members,omitempty"`
	Groups     []string    `json:"groups,omitempty"`
	Managers   []string    `json:"managers,omitempty"`
	ArchivedAt *time.Time  `json:"archivedAt,omitempty"`
	ArchivedBy string      `json:"archivedBy,omitempty"`
}

type Attachment struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Path     string `json:"path"`
}

type Message struct {
	AI             *AIMessage         `json:"ai,omitempty"`
	WithdrawnAt    *time.Time         `json:"withdrawnAt,omitempty"`
	WithdrawnBy    string             `json:"withdrawnBy,omitempty"`
	WithdrawnKind  string             `json:"withdrawnKind,omitempty"`
	ThreadRootSeq  int64              `json:"threadRootSeq,omitempty"`
	Seq            int64              `json:"seq"`
	ID             string             `json:"id"`
	Timestamp      time.Time          `json:"ts"`
	UserID         string             `json:"userId"`
	Text           string             `json:"text"`
	MentionUserIDs []string           `json:"mentionUserIds,omitempty"`
	ScheduleRef    *ScheduleReference `json:"scheduleRef,omitempty"`
	PollRef        *PollReference     `json:"pollRef,omitempty"`
	Attachments    []Attachment       `json:"attachments,omitempty"`
}

// AIMessage preserves attribution even after an account is removed from config.
type AIMessage struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	RequestID        string `json:"requestId"`
	TriggerMessageID string `json:"triggerMessageId"`
	RequestedBy      string `json:"requestedBy"`
	Kind             string `json:"kind"`
}

type Withdrawal struct {
	Version   int               `json:"version"`
	Kind      string            `json:"kind"`
	ChannelID string            `json:"channelId"`
	TargetID  string            `json:"targetId"`
	ActorID   string            `json:"actorId"`
	At        time.Time         `json:"at"`
	Events    []WithdrawalEvent `json:"events,omitempty"`
}

type WithdrawalEvent struct {
	Action  string    `json:"action"`
	ActorID string    `json:"actorId"`
	At      time.Time `json:"at"`
}

func (w *Withdrawal) Active() bool {
	return len(w.Events) == 0 || w.Events[len(w.Events)-1].Action == "withdraw"
}

func (w *Withdrawal) Valid() bool {
	if w.Kind == "" || w.ChannelID == "" || w.TargetID == "" || w.ActorID == "" || w.At.IsZero() {
		return false
	}
	if len(w.Events) == 0 {
		return true
	}
	lastWithdraw := WithdrawalEvent{}
	for i, event := range w.Events {
		want := "withdraw"
		if i%2 == 1 {
			want = "restore"
		}
		if event.Action != want || event.ActorID == "" || event.At.IsZero() {
			return false
		}
		if want == "withdraw" {
			lastWithdraw = event
		}
	}
	return lastWithdraw.ActorID == w.ActorID && lastWithdraw.At.Equal(w.At)
}

func (w *Withdrawal) Append(action, actorID string, at time.Time) {
	if len(w.Events) == 0 {
		w.Events = append(w.Events, WithdrawalEvent{Action: "withdraw", ActorID: w.ActorID, At: w.At})
	}
	w.Events = append(w.Events, WithdrawalEvent{Action: action, ActorID: actorID, At: at})
	if action == "withdraw" {
		w.ActorID, w.At = actorID, at
	}
}

type PollReference struct {
	ID string `json:"id"`
}

type PollOption struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type Poll struct {
	Version         int          `json:"version"`
	Revision        int64        `json:"revision"`
	ID              string       `json:"id"`
	ChannelID       string       `json:"channelId"`
	Question        string       `json:"question"`
	Description     string       `json:"description,omitempty"`
	Options         []PollOption `json:"options"`
	Multiple        bool         `json:"multiple"`
	Deadline        *time.Time   `json:"deadline,omitempty"`
	Status          string       `json:"status"`
	CreatedBy       string       `json:"createdBy"`
	CreatedAt       time.Time    `json:"createdAt"`
	UpdatedAt       time.Time    `json:"updatedAt"`
	AnnouncementSeq int64        `json:"announcementSeq,omitempty"`
}

type PollResponse struct {
	Version   int       `json:"version"`
	Seq       int64     `json:"seq"`
	UserID    string    `json:"userId"`
	OptionIDs []string  `json:"optionIds"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type ScheduleReference struct {
	ID    string `json:"id"`
	Event string `json:"event"`
}

type ScheduleStatus string

const (
	ScheduleDraft           ScheduleStatus = "draft"
	ScheduleOpen            ScheduleStatus = "open"
	ScheduleClosed          ScheduleStatus = "closed"
	ScheduleFinalized       ScheduleStatus = "finalized"
	ScheduleStatusWithdrawn ScheduleStatus = "withdrawn"
)

type ScheduleCandidate struct {
	ID        string `json:"id"`
	Date      string `json:"date"`
	StartTime string `json:"startTime,omitempty"`
	EndTime   string `json:"endTime,omitempty"`
}

type Schedule struct {
	Version              int                 `json:"version"`
	Revision             int64               `json:"revision"`
	ID                   string              `json:"id"`
	ChannelID            string              `json:"channelId"`
	Title                string              `json:"title"`
	Description          string              `json:"description,omitempty"`
	TimeZone             string              `json:"timeZone"`
	Candidates           []ScheduleCandidate `json:"candidates"`
	ResponseDeadline     *time.Time          `json:"responseDeadline,omitempty"`
	Status               ScheduleStatus      `json:"status"`
	CreatedBy            string              `json:"createdBy"`
	CreatedAt            time.Time           `json:"createdAt"`
	UpdatedAt            time.Time           `json:"updatedAt"`
	FinalCandidateID     string              `json:"finalCandidateId,omitempty"`
	AnnouncementSeq      int64               `json:"announcementSeq,omitempty"`
	FinalAnnouncementSeq int64               `json:"finalAnnouncementSeq,omitempty"`
}

type ScheduleChoice string

const (
	ScheduleYes   ScheduleChoice = "yes"
	ScheduleMaybe ScheduleChoice = "maybe"
	ScheduleNo    ScheduleChoice = "no"
)

type ScheduleResponse struct {
	Version   int                       `json:"version"`
	Seq       int64                     `json:"seq"`
	UserID    string                    `json:"userId"`
	Choices   map[string]ScheduleChoice `json:"choices"`
	Comment   string                    `json:"comment,omitempty"`
	UpdatedAt time.Time                 `json:"updatedAt"`
}

type Mention struct {
	Message   Message    `json:"message"`
	ChannelID string     `json:"channelId"`
	ReadAt    *time.Time `json:"readAt"`
}

// ReactionEvent is an append-only change to a user's reaction on a message.
type ReactionEvent struct {
	Version    int       `json:"version"`
	Timestamp  time.Time `json:"ts"`
	MessageSeq int64     `json:"messageSeq"`
	UserID     string    `json:"userId"`
	Key        string    `json:"key"`
	Active     bool      `json:"active"`
}

// ReactionState is the current set of users for one reaction on one message.
type ReactionState struct {
	MessageSeq int64    `json:"messageSeq"`
	Key        string   `json:"key"`
	UserIDs    []string `json:"userIds"`
}
