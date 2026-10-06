package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/masapico/minihub/internal/auth"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/service"
	"github.com/masapico/minihub/internal/storage"
)

const maxRequestBytes = 256 * 1024

type Authenticator interface {
	Authenticate(*http.Request) (string, error)
}

type SessionAuthenticator interface {
	Authenticator
	Login(http.ResponseWriter, *http.Request, string, string) (string, string, error)
	Logout(http.ResponseWriter, *http.Request) error
	CSRFToken(*http.Request) (string, bool)
	ValidateCSRF(*http.Request) bool
	RefreshCurrentSession(*http.Request, uint64) error
}

// HeaderAuthenticator is intended for development and trusted reverse proxies.
// A deployment must strip client-supplied identity headers before setting its own.
type HeaderAuthenticator struct{ Header string }

func (a HeaderAuthenticator) Authenticate(r *http.Request) (string, error) {
	header := a.Header
	if header == "" {
		header = "X-User-ID"
	}
	id := strings.TrimSpace(r.Header.Get(header))
	if id == "" {
		return "", service.ErrUnauthenticated
	}
	return id, nil
}

type API struct {
	service *service.Service
	auth    Authenticator
	logger  *slog.Logger
}

func New(svc *service.Service, auth Authenticator, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &API{service: svc, auth: auth, logger: logger}
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.URL.Path == "/api/auth/login" && r.Method == http.MethodPost {
		a.login(w, r)
		return
	}
	userID, err := a.auth.Authenticate(r)
	if err != nil {
		a.writeError(w, service.ErrUnauthenticated)
		return
	}
	if sessionAuth, ok := a.auth.(SessionAuthenticator); ok {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !sessionAuth.ValidateCSRF(r) {
			a.writeJSON(w, http.StatusForbidden, map[string]string{"error": "セキュリティトークンが無効です。画面を再読み込みして、もう一度お試しください"})
			return
		}
		if r.URL.Path == "/api/auth/logout" && r.Method == http.MethodPost {
			if err := sessionAuth.Logout(w, r); err != nil {
				a.writeError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	if a.threadRoutes(w, r, userID, parts) {
		return
	}
	if path == "api/me" && r.Method == http.MethodGet {
		a.me(w, r, userID)
		return
	}
	if path == "api/me/password" && r.Method == http.MethodPut {
		a.changePassword(w, r, userID)
		return
	}
	if path == "api/mentions" && r.Method == http.MethodGet {
		a.mentions(w, r, userID)
		return
	}
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "mentions" && parts[3] == "read" && r.Method == http.MethodPut {
		a.markMentionRead(w, r, userID, parts[2])
		return
	}
	if path == "api/schedules" {
		switch r.Method {
		case http.MethodGet:
			a.listSchedules(w, r, userID)
		case http.MethodPost:
			a.createSchedule(w, r, userID)
		default:
			a.methodNotAllowed(w, http.MethodGet, http.MethodPost)
		}
		return
	}
	if path == "api/polls" {
		a.polls(w, r, userID)
		return
	}
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "polls" && parts[2] != "" {
		a.poll(w, r, userID, parts)
		return
	}
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "schedules" && parts[2] != "" {
		scheduleID := parts[2]
		if len(parts) == 3 {
			switch r.Method {
			case http.MethodGet:
				a.getSchedule(w, r, userID, scheduleID)
			case http.MethodPut:
				a.updateSchedule(w, r, userID, scheduleID)
			default:
				a.methodNotAllowed(w, http.MethodGet, http.MethodPut)
			}
			return
		}
		if len(parts) == 4 && r.Method == http.MethodPost {
			if parts[3] == "withdraw" {
				detail, err := a.service.WithdrawSchedule(r.Context(), userID, scheduleID)
				if err != nil {
					a.writeError(w, err)
				} else {
					a.writeJSON(w, http.StatusOK, detail)
				}
				return
			}
			if parts[3] == "restore" {
				detail, err := a.service.RestoreSchedule(r.Context(), userID, scheduleID)
				if err != nil {
					a.writeError(w, err)
				} else {
					a.writeJSON(w, http.StatusOK, detail)
				}
				return
			}
			a.scheduleAction(w, r, userID, scheduleID, parts[3])
			return
		}
		if len(parts) == 5 && parts[3] == "responses" && parts[4] == "me" && r.Method == http.MethodPut {
			a.setScheduleResponse(w, r, userID, scheduleID)
			return
		}
	}
	if path == "api/channels" {
		switch r.Method {
		case http.MethodGet:
			a.listChannels(w, r, userID)
		case http.MethodPost:
			a.createChannel(w, r, userID)
		default:
			a.methodNotAllowed(w, http.MethodGet, http.MethodPost)
		}
		return
	}
	if path == "api/groups/directory" && r.Method == http.MethodGet {
		a.groupDirectory(w, r, userID)
		return
	}
	if path == "api/users" {
		switch r.Method {
		case http.MethodGet:
			a.listUsers(w, r, userID)
		case http.MethodPost:
			a.createUsers(w, r, userID)
		default:
			a.methodNotAllowed(w, http.MethodGet, http.MethodPost)
		}
		return
	}
	if path == "api/users/directory" && r.Method == http.MethodGet {
		a.userDirectory(w, r, userID)
		return
	}
	if path == "api/groups" {
		switch r.Method {
		case http.MethodGet:
			a.listGroups(w, r, userID)
		case http.MethodPost:
			a.createGroup(w, r, userID)
		default:
			a.methodNotAllowed(w, http.MethodGet, http.MethodPost)
		}
		return
	}
	if path == "api/channels/read-statuses" {
		if r.Method == http.MethodGet {
			a.readStatuses(w, r, userID)
		} else {
			a.methodNotAllowed(w, http.MethodGet)
		}
		return
	}
	if len(parts) == 3 && parts[0] == "api" && parts[1] == "groups" && parts[2] != "" {
		switch r.Method {
		case http.MethodGet:
			a.getGroup(w, r, userID, parts[2])
			return
		case http.MethodPut:
			a.updateGroup(w, r, userID, parts[2])
			return
		case http.MethodDelete:
			a.deleteGroup(w, r, userID, parts[2])
			return
		}
		a.methodNotAllowed(w, http.MethodGet, http.MethodPut, http.MethodDelete)
		return
	}
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "groups" && parts[2] != "" && parts[3] == "members" {
		if r.Method == http.MethodPatch {
			a.changeGroupMembers(w, r, userID, parts[2])
			return
		}
		a.methodNotAllowed(w, http.MethodPatch)
		return
	}
	if len(parts) == 3 && parts[0] == "api" && parts[1] == "users" && parts[2] != "" {
		if r.Method == http.MethodPut {
			a.updateUser(w, r, userID, parts[2])
			return
		}
		a.methodNotAllowed(w, http.MethodPut)
		return
	}
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "channels" && parts[2] != "" {
		channelID := parts[2]
		if len(parts) == 3 {
			switch r.Method {
			case http.MethodGet:
				a.getChannel(w, r, userID, channelID)
			case http.MethodPatch:
				a.updateChannel(w, r, userID, channelID)
			default:
				a.methodNotAllowed(w, http.MethodGet, http.MethodPatch)
			}
			return
		}
		if len(parts) == 4 {
			switch parts[3] {
			case "management":
				if r.Method == http.MethodGet {
					a.getChannelManagement(w, r, userID, channelID)
					return
				}
			case "members":
				if r.Method == http.MethodPatch {
					a.changeChannelMembers(w, r, userID, channelID)
					return
				}
			case "archive":
				if r.Method == http.MethodPost {
					a.archiveChannel(w, r, userID, channelID)
					return
				}
			case "restore":
				if r.Method == http.MethodPost {
					a.restoreChannel(w, r, userID, channelID)
					return
				}
			case "join":
				if r.Method == http.MethodPost {
					a.join(w, r, userID, channelID)
					return
				}
			case "leave":
				if r.Method == http.MethodPost {
					a.leave(w, r, userID, channelID)
					return
				}
			case "messages":
				switch r.Method {
				case http.MethodGet:
					a.messages(w, r, userID, channelID)
					return
				case http.MethodPost:
					a.postMessage(w, r, userID, channelID)
					return
				}
			case "read":
				if r.Method == http.MethodGet {
					a.readStatus(w, r, userID, channelID)
					return
				}
				if r.Method == http.MethodPut {
					a.markRead(w, r, userID, channelID)
					return
				}
			case "reactions":
				if r.Method == http.MethodGet {
					a.reactions(w, r, userID, channelID)
					return
				}
			}
		}
		if len(parts) == 6 && parts[3] == "messages" && parts[5] == "withdraw" && r.Method == http.MethodPost {
			seq, err := strconv.ParseInt(parts[4], 10, 64)
			if err != nil {
				a.writeError(w, fmt.Errorf("%w: メッセージ番号が正しくありません", service.ErrInvalid))
				return
			}
			message, err := a.service.WithdrawMessage(r.Context(), userID, channelID, seq)
			if err != nil {
				a.writeError(w, err)
			} else {
				a.writeJSON(w, http.StatusOK, message)
			}
			return
		}
		if len(parts) == 6 && parts[3] == "messages" && parts[5] == "restore" && r.Method == http.MethodPost {
			seq, err := strconv.ParseInt(parts[4], 10, 64)
			if err != nil {
				a.writeError(w, fmt.Errorf("%w: メッセージ番号が正しくありません", service.ErrInvalid))
				return
			}
			message, err := a.service.RestoreMessage(r.Context(), userID, channelID, seq)
			if err != nil {
				a.writeError(w, err)
			} else {
				a.writeJSON(w, http.StatusOK, message)
			}
			return
		}
		if len(parts) == 7 && parts[3] == "messages" && parts[5] == "reactions" {
			seq, err := strconv.ParseInt(parts[4], 10, 64)
			if err != nil {
				a.writeError(w, fmt.Errorf("%w: メッセージ番号には整数を指定してください", service.ErrInvalid))
				return
			}
			switch r.Method {
			case http.MethodPut:
				a.setReaction(w, r, userID, channelID, seq, parts[6], true)
				return
			case http.MethodDelete:
				a.setReaction(w, r, userID, channelID, seq, parts[6], false)
				return
			default:
				a.methodNotAllowed(w, http.MethodPut, http.MethodDelete)
				return
			}
		}
		if len(parts) == 8 && parts[3] == "messages" && parts[5] == "reactions" && parts[7] == "users" {
			if r.Method != http.MethodGet {
				a.methodNotAllowed(w, http.MethodGet)
				return
			}
			seq, err := strconv.ParseInt(parts[4], 10, 64)
			if err != nil {
				a.writeError(w, fmt.Errorf("%w: メッセージ番号には整数を指定してください", service.ErrInvalid))
				return
			}
			a.reactionUsers(w, r, userID, channelID, seq, parts[6])
			return
		}
	}
	a.writeJSON(w, http.StatusNotFound, map[string]string{"error": "指定された情報が見つかりません"})
}

func (a *API) listUsers(w http.ResponseWriter, r *http.Request, actorID string) {
	users, err := a.service.ListUsers(r.Context(), actorID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	out := make([]userResponse, len(users))
	for i := range users {
		out[i] = userView(&users[i], "")
	}
	a.writeJSON(w, http.StatusOK, out)
}

func (a *API) userDirectory(w http.ResponseWriter, r *http.Request, userID string) {
	users, err := a.service.ListUserDirectory(r.Context(), userID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	type entry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := make([]entry, len(users))
	for i := range users {
		out[i] = entry{ID: users[i].ID, Name: users[i].Name}
	}
	a.writeJSON(w, http.StatusOK, out)
}

func (a *API) listGroups(w http.ResponseWriter, r *http.Request, userID string) {
	groups, err := a.service.ListGroups(r.Context(), userID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, groups)
}

func (a *API) createGroup(w http.ResponseWriter, r *http.Request, actorID string) {
	var input struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if !a.decode(w, r, &input) {
		return
	}
	group, err := a.service.CreateGroup(r.Context(), actorID, domain.Group{ID: input.ID, Name: input.Name})
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, group)
}

func (a *API) getGroup(w http.ResponseWriter, r *http.Request, actorID, groupID string) {
	detail, err := a.service.GetGroupDetail(r.Context(), actorID, groupID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, detail)
}

func (a *API) updateGroup(w http.ResponseWriter, r *http.Request, actorID, groupID string) {
	var input struct {
		Name string `json:"name"`
	}
	if !a.decode(w, r, &input) {
		return
	}
	group, err := a.service.UpdateGroup(r.Context(), actorID, groupID, input.Name)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, group)
}

func (a *API) changeGroupMembers(w http.ResponseWriter, r *http.Request, actorID, groupID string) {
	var input struct {
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}
	if !a.decode(w, r, &input) {
		return
	}
	detail, err := a.service.ChangeGroupMembers(r.Context(), actorID, groupID, input.Add, input.Remove)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, detail)
}

func (a *API) deleteGroup(w http.ResponseWriter, r *http.Request, actorID, groupID string) {
	if err := a.service.DeleteGroup(r.Context(), actorID, groupID); err != nil {
		a.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) createUsers(w http.ResponseWriter, r *http.Request, actorID string) {
	var input struct {
		Users []struct {
			ID       string      `json:"id"`
			Name     string      `json:"name"`
			Password string      `json:"password"`
			Groups   []string    `json:"groups"`
			Role     domain.Role `json:"role"`
		} `json:"users"`
	}
	if !a.decode(w, r, &input) {
		return
	}
	items := make([]service.NewUser, len(input.Users))
	for i, user := range input.Users {
		items[i] = service.NewUser{ID: user.ID, Name: user.Name, Password: user.Password, Groups: user.Groups, Role: user.Role}
	}
	users, err := a.service.CreateUsers(r.Context(), actorID, items)
	if err != nil {
		a.writeError(w, err)
		return
	}
	out := make([]userResponse, len(users))
	for i := range users {
		out[i] = userView(&users[i], "")
	}
	a.writeJSON(w, http.StatusCreated, out)
}

func (a *API) updateUser(w http.ResponseWriter, r *http.Request, actorID, userID string) {
	var input struct {
		Name     string      `json:"name"`
		Password string      `json:"password"`
		Groups   *[]string   `json:"groups"`
		Role     domain.Role `json:"role"`
		Enabled  bool        `json:"enabled"`
	}
	if !a.decode(w, r, &input) {
		return
	}
	update := service.UpdateUser{Name: input.Name, Password: input.Password, Role: input.Role, Enabled: input.Enabled}
	if input.Groups != nil {
		update.Groups = *input.Groups
		update.GroupsProvided = true
	}
	user, err := a.service.UpdateUser(r.Context(), actorID, userID, update)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, userView(user, ""))
}

type userResponse struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Groups       []string      `json:"groups,omitempty"`
	Role         domain.Role   `json:"role"`
	Enabled      bool          `json:"enabled"`
	CSRFToken    string        `json:"csrfToken,omitempty"`
	Capabilities *capabilities `json:"capabilities,omitempty"`
}

type capabilities struct {
	SelfPasswordChange bool `json:"selfPasswordChange"`
}

func userView(user *domain.User, csrf string) userResponse {
	return userResponse{ID: user.ID, Name: user.Name, Groups: user.Groups, Role: user.Role, Enabled: user.Enabled, CSRFToken: csrf}
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	sessionAuth, ok := a.auth.(SessionAuthenticator)
	if !ok {
		a.writeJSON(w, http.StatusNotFound, map[string]string{"error": "指定された情報が見つかりません"})
		return
	}
	var input struct {
		UserID   string `json:"userId"`
		Password string `json:"password"`
	}
	if !a.decode(w, r, &input) {
		return
	}
	userID, csrf, err := sessionAuth.Login(w, r, input.UserID, input.Password)
	if errors.Is(err, auth.ErrRateLimited) {
		a.writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "ログイン試行回数が多すぎます。しばらく待ってから、もう一度お試しください"})
		return
	}
	if err != nil {
		a.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "ユーザーIDまたはパスワードが正しくありません"})
		return
	}
	user, err := a.service.Me(r.Context(), userID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	view := userView(user, csrf)
	view.Capabilities = &capabilities{SelfPasswordChange: a.service.SelfPasswordChangeEnabled()}
	a.writeJSON(w, http.StatusOK, view)
}

func (a *API) me(w http.ResponseWriter, r *http.Request, userID string) {
	user, err := a.service.Me(r.Context(), userID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	csrf := ""
	if sessionAuth, ok := a.auth.(SessionAuthenticator); ok {
		csrf, _ = sessionAuth.CSRFToken(r)
	}
	view := userView(user, csrf)
	view.Capabilities = &capabilities{SelfPasswordChange: a.service.SelfPasswordChangeEnabled()}
	a.writeJSON(w, http.StatusOK, view)
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request, userID string) {
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !a.decode(w, r, &input) {
		return
	}
	generation, err := a.service.ChangePassword(r.Context(), userID, input.CurrentPassword, input.NewPassword)
	if err != nil {
		a.writeError(w, err)
		return
	}
	sessionAuth, ok := a.auth.(SessionAuthenticator)
	if !ok {
		a.writeError(w, service.ErrForbidden)
		return
	}
	if err := sessionAuth.RefreshCurrentSession(r, generation); err != nil {
		a.writeError(w, err)
		return
	}
	a.service.PasswordChanged(userID)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) mentions(w http.ResponseWriter, r *http.Request, userID string) {
	limit, err := queryInt(r, "limit", 50)
	if err != nil {
		a.writeError(w, err)
		return
	}
	page, err := a.service.ListMentions(r.Context(), userID, r.URL.Query().Get("cursor"), int(limit))
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, page)
}

func (a *API) markMentionRead(w http.ResponseWriter, r *http.Request, userID, messageID string) {
	if err := a.service.MarkMentionRead(r.Context(), userID, messageID); err != nil {
		a.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listChannels(w http.ResponseWriter, r *http.Request, userID string) {
	var channels []domain.Channel
	var err error
	if r.URL.Query().Get("management") == "true" {
		channels, err = a.service.ListManageableChannels(r.Context(), userID, r.URL.Query().Get("includeArchived") == "true")
	} else {
		channels, err = a.service.ListChannels(r.Context(), userID)
	}
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, channels)
}

func (a *API) createChannel(w http.ResponseWriter, r *http.Request, userID string) {
	var input struct {
		ID      string             `json:"id"`
		Name    string             `json:"name"`
		Type    domain.ChannelType `json:"type"`
		Members []string           `json:"members"`
		Groups  []string           `json:"groups"`
	}
	if !a.decode(w, r, &input) {
		return
	}
	channel, err := a.service.CreateChannel(r.Context(), userID, domain.Channel{ID: input.ID, Name: input.Name, Type: input.Type, Members: input.Members, Groups: input.Groups})
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, channel)
}

func (a *API) getChannel(w http.ResponseWriter, r *http.Request, userID, channelID string) {
	channel, err := a.service.GetChannel(r.Context(), userID, channelID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	users, err := a.service.ListUserDirectory(r.Context(), userID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	type effectiveMember struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		Direct    bool     `json:"direct,omitempty"`
		ViaGroups []string `json:"viaGroups,omitempty"`
	}
	type channelDetail struct {
		*domain.Channel
		EffectiveMembers []effectiveMember           `json:"effectiveMembers"`
		Capabilities     service.ChannelCapabilities `json:"capabilities"`
	}
	direct := make(map[string]bool, len(channel.Members))
	for _, id := range channel.Members {
		direct[id] = true
	}
	groups := make(map[string]bool, len(channel.Groups))
	for _, id := range channel.Groups {
		groups[id] = true
	}
	members := make([]effectiveMember, 0, len(users))
	for _, user := range users {
		via := make([]string, 0, len(user.Groups))
		for _, groupID := range user.Groups {
			if groups[groupID] {
				via = append(via, groupID)
			}
		}
		if direct[user.ID] || len(via) > 0 {
			members = append(members, effectiveMember{ID: user.ID, Name: user.Name, Direct: direct[user.ID], ViaGroups: via})
		}
	}
	me, err := a.service.Me(r.Context(), userID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	manager := channel.CreatedBy == userID && len(channel.Managers) == 0
	for _, id := range channel.Managers {
		if id == userID {
			manager = true
			break
		}
	}
	admin := me.Role == domain.RoleAdmin
	a.writeJSON(w, http.StatusOK, channelDetail{Channel: channel, EffectiveMembers: members, Capabilities: service.ChannelCapabilities{ManageMembers: admin || manager, ManageSettings: admin, Archive: admin}})
}

func (a *API) getChannelManagement(w http.ResponseWriter, r *http.Request, userID, channelID string) {
	detail, err := a.service.GetChannelManagement(r.Context(), userID, channelID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, detail)
}

func (a *API) updateChannel(w http.ResponseWriter, r *http.Request, userID, channelID string) {
	var input struct {
		Name *string             `json:"name"`
		Type *domain.ChannelType `json:"type"`
	}
	if !a.decode(w, r, &input) {
		return
	}
	current, err := a.service.GetChannelManagement(r.Context(), userID, channelID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	name, channelType := current.Name, current.Type
	if input.Name != nil {
		name = *input.Name
	}
	if input.Type != nil {
		channelType = *input.Type
	}
	channel, err := a.service.UpdateChannel(r.Context(), userID, channelID, name, channelType)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.logger.Info("channel settings changed", "actorId", userID, "channelId", channelID, "name", channel.Name, "type", channel.Type)
	a.writeJSON(w, http.StatusOK, channel)
}

func (a *API) changeChannelMembers(w http.ResponseWriter, r *http.Request, userID, channelID string) {
	var input struct {
		AddUsers       []string `json:"addUsers"`
		RemoveUsers    []string `json:"removeUsers"`
		AddGroups      []string `json:"addGroups"`
		RemoveGroups   []string `json:"removeGroups"`
		AddManagers    []string `json:"addManagers"`
		RemoveManagers []string `json:"removeManagers"`
	}
	if !a.decode(w, r, &input) {
		return
	}
	changes := service.ChannelMemberChanges{AddUsers: input.AddUsers, RemoveUsers: input.RemoveUsers, AddGroups: input.AddGroups, RemoveGroups: input.RemoveGroups, AddManagers: input.AddManagers, RemoveManagers: input.RemoveManagers}
	detail, err := a.service.ChangeChannelMembers(r.Context(), userID, channelID, changes)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.logger.Info("channel membership changed", "actorId", userID, "channelId", channelID, "addUsers", input.AddUsers, "removeUsers", input.RemoveUsers, "addGroups", input.AddGroups, "removeGroups", input.RemoveGroups, "addManagers", input.AddManagers, "removeManagers", input.RemoveManagers)
	a.writeJSON(w, http.StatusOK, detail)
}

func (a *API) archiveChannel(w http.ResponseWriter, r *http.Request, userID, channelID string) {
	if err := a.service.ArchiveChannel(r.Context(), userID, channelID); err != nil {
		a.writeError(w, err)
		return
	}
	a.logger.Info("channel archived", "actorId", userID, "channelId", channelID)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) restoreChannel(w http.ResponseWriter, r *http.Request, userID, channelID string) {
	if err := a.service.RestoreChannel(r.Context(), userID, channelID); err != nil {
		a.writeError(w, err)
		return
	}
	a.logger.Info("channel restored", "actorId", userID, "channelId", channelID)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) groupDirectory(w http.ResponseWriter, r *http.Request, userID string) {
	groups, err := a.service.ListGroups(r.Context(), userID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	type entry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := make([]entry, len(groups))
	for i := range groups {
		out[i] = entry{ID: groups[i].ID, Name: groups[i].Name}
	}
	a.writeJSON(w, http.StatusOK, out)
}

func (a *API) join(w http.ResponseWriter, r *http.Request, userID, channelID string) {
	channel, err := a.service.JoinChannel(r.Context(), userID, channelID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, channel)
}

func (a *API) leave(w http.ResponseWriter, r *http.Request, userID, channelID string) {
	channel, err := a.service.LeaveChannel(r.Context(), userID, channelID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, channel)
}

func (a *API) messages(w http.ResponseWriter, r *http.Request, userID, channelID string) {
	before, err := queryOptionalInt(r, "before")
	if err != nil {
		a.writeError(w, err)
		return
	}
	after, err := queryOptionalInt(r, "after")
	if err != nil {
		a.writeError(w, err)
		return
	}
	around, err := queryOptionalInt(r, "around")
	if err != nil {
		a.writeError(w, err)
		return
	}
	limit, err := queryInt(r, "limit", 100)
	if err != nil {
		a.writeError(w, err)
		return
	}
	messages, err := a.service.GetMessages(r.Context(), userID, channelID, storage.MessageQuery{BeforeSeq: before, AfterSeq: after, AroundSeq: around, Limit: int(limit)})
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, messages)
}

func (a *API) postMessage(w http.ResponseWriter, r *http.Request, userID, channelID string) {
	var input struct {
		Text string `json:"text"`
	}
	if !a.decode(w, r, &input) {
		return
	}
	message, err := a.service.PostMessage(r.Context(), userID, channelID, input.Text)
	if err != nil {
		var committed *service.CommittedMessageError
		if !errors.As(err, &committed) {
			a.writeError(w, err)
			return
		}
		a.logger.Error("message committed but author read state was not updated", "userId", userID, "channelId", channelID, "seq", message.Seq, "error", committed)
	}
	a.writeJSON(w, http.StatusCreated, message)
}

func (a *API) reactions(w http.ResponseWriter, r *http.Request, userID, channelID string) {
	if values, ok := r.URL.Query()["messageSeqs"]; ok {
		if len(values) != 1 || values[0] == "" || r.URL.Query().Has("afterMessageSeq") {
			a.writeError(w, fmt.Errorf("%w: リアクションの取得条件が正しくありません", service.ErrInvalid))
			return
		}
		parts := strings.Split(values[0], ",")
		if len(parts) > 101 {
			a.writeError(w, fmt.Errorf("%w: メッセージ番号が多すぎます", service.ErrInvalid))
			return
		}
		seqs := make([]int64, 0, len(parts))
		for _, part := range parts {
			seq, err := strconv.ParseInt(part, 10, 64)
			if err != nil || seq < 1 {
				a.writeError(w, fmt.Errorf("%w: メッセージ番号が正しくありません", service.ErrInvalid))
				return
			}
			seqs = append(seqs, seq)
		}
		reactions, err := a.service.GetReactionsForMessages(r.Context(), userID, channelID, seqs)
		if err != nil {
			a.writeError(w, err)
		} else {
			a.writeJSON(w, http.StatusOK, reactions)
		}
		return
	}
	after, err := queryInt(r, "afterMessageSeq", 0)
	if err != nil {
		a.writeError(w, err)
		return
	}
	reactions, err := a.service.GetReactions(r.Context(), userID, channelID, after)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, reactions)
}

func (a *API) reactionUsers(w http.ResponseWriter, r *http.Request, userID, channelID string, seq int64, key string) {
	userIDs, err := a.service.GetReactionUsers(r.Context(), userID, channelID, seq, key)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, struct {
		UserIDs []string `json:"userIds"`
	}{UserIDs: userIDs})
}

func (a *API) setReaction(w http.ResponseWriter, r *http.Request, userID, channelID string, seq int64, key string, active bool) {
	reactions, err := a.service.SetReaction(r.Context(), userID, channelID, seq, key, active)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, reactions)
}

func (a *API) markRead(w http.ResponseWriter, r *http.Request, userID, channelID string) {
	var input struct {
		Seq int64 `json:"seq"`
	}
	if !a.decode(w, r, &input) {
		return
	}
	if err := a.service.MarkRead(r.Context(), userID, channelID, input.Seq); err != nil {
		a.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) readStatus(w http.ResponseWriter, r *http.Request, userID, channelID string) {
	status, err := a.service.GetReadStatus(r.Context(), userID, channelID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, status)
}

func (a *API) readStatuses(w http.ResponseWriter, r *http.Request, userID string) {
	statuses, err := a.service.GetReadStatuses(r.Context(), userID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, struct {
		Statuses map[string]service.ReadStatus `json:"statuses"`
	}{Statuses: statuses})
}

func queryInt(r *http.Request, name string, fallback int64) (int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: クエリパラメーター %s には整数を指定してください", service.ErrInvalid, name)
	}
	return value, nil
}

func queryOptionalInt(r *http.Request, name string) (*int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: クエリパラメーター %s には整数を指定してください", service.ErrInvalid, name)
	}
	return &value, nil
}

func (a *API) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		a.writeError(w, fmt.Errorf("%w: リクエスト本文のJSON形式が正しくありません", service.ErrInvalid))
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		a.writeError(w, fmt.Errorf("%w: リクエスト本文にはJSON値を1つだけ指定してください", service.ErrInvalid))
		return false
	}
	return true
}

func (a *API) methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	a.writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "この操作は許可されていません"})
}

func (a *API) writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "サーバー内部でエラーが発生しました。しばらく待ってから、もう一度お試しください"
	switch {
	case errors.Is(err, service.ErrUnauthenticated):
		status, message = http.StatusUnauthorized, "ログインが必要です"
	case errors.Is(err, service.ErrForbidden):
		status, message = http.StatusForbidden, "この操作を行う権限がありません"
	case errors.Is(err, service.ErrNotFound):
		status, message = http.StatusNotFound, "指定された情報が見つかりません"
	case errors.Is(err, service.ErrConflict):
		status, message = http.StatusConflict, "IDは重複できません"
	case errors.Is(err, service.ErrStale):
		status, message = http.StatusConflict, "予定調整が更新されています。再読み込みしてください"
	case errors.Is(err, service.ErrInvalid):
		status, message = http.StatusBadRequest, strings.TrimPrefix(err.Error(), service.ErrInvalid.Error()+": ")
		if message == service.ErrInvalid.Error() || message == "" {
			message = "リクエストの内容が正しくありません"
		}
	default:
		a.logger.Error("request failed", "error", err)
	}
	a.writeJSON(w, status, map[string]string{"error": message})
}

func (a *API) writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	if status != http.StatusNoContent {
		_ = json.NewEncoder(w).Encode(value)
	}
}
