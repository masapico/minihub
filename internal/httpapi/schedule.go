package httpapi

import (
	"net/http"
	"time"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/service"
)

type scheduleInput struct {
	Revision         int64                      `json:"revision,omitempty"`
	ChannelID        string                     `json:"channelId,omitempty"`
	Title            string                     `json:"title"`
	Description      string                     `json:"description,omitempty"`
	Candidates       []domain.ScheduleCandidate `json:"candidates"`
	ResponseDeadline *time.Time                 `json:"responseDeadline,omitempty"`
}

func (a *API) listSchedules(w http.ResponseWriter, r *http.Request, userID string) {
	limit, err := queryInt(r, "limit", 50)
	if err != nil {
		a.writeError(w, err)
		return
	}
	page, err := a.service.ListSchedulesByPeriod(r.Context(), userID, r.URL.Query().Get("cursor"), int(limit), r.URL.Query().Get("period"))
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, page)
}

func (a *API) createSchedule(w http.ResponseWriter, r *http.Request, userID string) {
	var input scheduleInput
	if !a.decode(w, r, &input) {
		return
	}
	schedule, err := a.service.CreateSchedule(r.Context(), userID, service.NewSchedule{ChannelID: input.ChannelID, Title: input.Title, Description: input.Description, Candidates: input.Candidates, ResponseDeadline: input.ResponseDeadline})
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, schedule)
}

func (a *API) getSchedule(w http.ResponseWriter, r *http.Request, userID, scheduleID string) {
	detail, err := a.service.GetSchedule(r.Context(), userID, scheduleID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, detail)
}

func (a *API) updateSchedule(w http.ResponseWriter, r *http.Request, userID, scheduleID string) {
	var input scheduleInput
	if !a.decode(w, r, &input) {
		return
	}
	schedule, err := a.service.UpdateSchedule(r.Context(), userID, scheduleID, service.UpdateSchedule{Revision: input.Revision, Title: input.Title, Description: input.Description, Candidates: input.Candidates, ResponseDeadline: input.ResponseDeadline})
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, schedule)
}

func (a *API) scheduleAction(w http.ResponseWriter, r *http.Request, userID, scheduleID, action string) {
	var input struct {
		Revision         int64      `json:"revision"`
		ResponseDeadline *time.Time `json:"responseDeadline,omitempty"`
		CandidateID      string     `json:"candidateId,omitempty"`
	}
	if !a.decode(w, r, &input) {
		return
	}
	var schedule *domain.Schedule
	var err error
	switch action {
	case "publish":
		schedule, err = a.service.PublishSchedule(r.Context(), userID, scheduleID, input.Revision)
	case "close":
		schedule, err = a.service.CloseSchedule(r.Context(), userID, scheduleID, input.Revision)
	case "reopen":
		schedule, err = a.service.ReopenSchedule(r.Context(), userID, scheduleID, input.Revision, input.ResponseDeadline)
	case "finalize":
		schedule, err = a.service.FinalizeSchedule(r.Context(), userID, scheduleID, input.Revision, input.CandidateID)
	default:
		a.writeJSON(w, http.StatusNotFound, map[string]string{"error": "指定された情報が見つかりません"})
		return
	}
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, schedule)
}

func (a *API) setScheduleResponse(w http.ResponseWriter, r *http.Request, userID, scheduleID string) {
	var input struct {
		Revision int64                            `json:"revision"`
		Choices  map[string]domain.ScheduleChoice `json:"choices"`
		Comment  string                           `json:"comment,omitempty"`
	}
	if !a.decode(w, r, &input) {
		return
	}
	response, err := a.service.SetScheduleResponse(r.Context(), userID, scheduleID, input.Revision, input.Choices, input.Comment)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, response)
}
