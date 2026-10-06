package httpapi

import (
	"github.com/masapico/minihub/internal/service"
	"net/http"
)

func (a *API) polls(w http.ResponseWriter, r *http.Request, user string) {
	switch r.Method {
	case http.MethodGet:
		result, err := a.service.ListPolls(r.Context(), user, r.URL.Query().Get("channelId"), r.URL.Query().Get("active") == "true")
		if err != nil {
			a.writeError(w, err)
			return
		}
		a.writeJSON(w, http.StatusOK, result)
	case http.MethodPost:
		var in service.NewPoll
		if !a.decode(w, r, &in) {
			return
		}
		result, err := a.service.CreatePoll(r.Context(), user, in)
		if err != nil {
			a.writeError(w, err)
			return
		}
		a.writeJSON(w, http.StatusCreated, result)
	default:
		a.methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}
func (a *API) poll(w http.ResponseWriter, r *http.Request, user string, parts []string) {
	id := parts[2]
	if len(parts) == 4 && parts[3] == "withdraw" && r.Method == http.MethodPost {
		d, err := a.service.WithdrawPoll(r.Context(), user, id)
		if err != nil {
			a.writeError(w, err)
		} else {
			a.writeJSON(w, http.StatusOK, d)
		}
		return
	}
	if len(parts) == 4 && parts[3] == "restore" && r.Method == http.MethodPost {
		d, err := a.service.RestorePoll(r.Context(), user, id)
		if err != nil {
			a.writeError(w, err)
		} else {
			a.writeJSON(w, http.StatusOK, d)
		}
		return
	}
	if len(parts) == 3 && r.Method == http.MethodGet {
		d, err := a.service.GetPoll(r.Context(), user, id)
		if err != nil {
			a.writeError(w, err)
			return
		}
		a.writeJSON(w, http.StatusOK, d)
		return
	}
	if len(parts) == 4 && parts[3] == "vote" && r.Method == http.MethodPut {
		var in struct {
			Revision  int64    `json:"revision"`
			OptionIDs []string `json:"optionIds"`
		}
		if !a.decode(w, r, &in) {
			return
		}
		d, err := a.service.VotePoll(r.Context(), user, id, in.Revision, in.OptionIDs)
		if err != nil {
			a.writeError(w, err)
			return
		}
		a.writeJSON(w, http.StatusOK, d)
		return
	}
	if len(parts) == 4 && parts[3] == "close" && r.Method == http.MethodPost {
		var in struct {
			Revision int64 `json:"revision"`
		}
		if !a.decode(w, r, &in) {
			return
		}
		d, err := a.service.ClosePoll(r.Context(), user, id, in.Revision)
		if err != nil {
			a.writeError(w, err)
			return
		}
		a.writeJSON(w, http.StatusOK, d)
		return
	}
	a.writeJSON(w, http.StatusNotFound, map[string]string{"error": "指定された情報が見つかりません"})
}
