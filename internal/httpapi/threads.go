package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/masapico/minihub/internal/service"
	"github.com/masapico/minihub/internal/storage"
)

func (a *API) threadRoutes(w http.ResponseWriter, r *http.Request, userID string, parts []string) bool {
	if len(parts) == 2 && parts[0] == "api" && parts[1] == "threads" {
		a.listThreads(w, r, userID, "")
		return true
	}
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "channels" && parts[3] == "thread-summaries" {
		if r.Method != http.MethodGet {
			a.methodNotAllowed(w, http.MethodGet)
			return true
		}
		raw := strings.Split(r.URL.Query().Get("roots"), ",")
		if len(raw) > 500 {
			a.writeError(w, fmt.Errorf("%w: 対象が多すぎます", service.ErrInvalid))
			return true
		}
		roots := make(map[int64]bool, len(raw))
		for _, value := range raw {
			seq, err := strconv.ParseInt(value, 10, 64)
			if err != nil || seq < 1 {
				a.writeError(w, fmt.Errorf("%w: スレッド番号が正しくありません", service.ErrInvalid))
				return true
			}
			roots[seq] = true
		}
		summaries, err := a.service.ThreadSummaries(r.Context(), userID, parts[2], roots)
		if err != nil {
			a.writeError(w, err)
		} else {
			a.writeJSON(w, http.StatusOK, map[string]any{"threads": summaries})
		}
		return true
	}
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "channels" || parts[3] != "threads" {
		return false
	}
	channel := parts[2]
	if len(parts) == 4 {
		a.listThreads(w, r, userID, channel)
		return true
	}
	if len(parts) != 6 {
		return false
	}
	root, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil || root < 1 {
		a.writeError(w, fmt.Errorf("%w: スレッド番号が正しくありません", service.ErrInvalid))
		return true
	}
	switch parts[5] {
	case "messages":
		switch r.Method {
		case http.MethodGet:
			before, e1 := queryOptionalInt(r, "before")
			after, e2 := queryOptionalInt(r, "after")
			around, e3 := queryOptionalInt(r, "around")
			limit, e4 := queryInt(r, "limit", 50)
			for _, err := range []error{e1, e2, e3, e4} {
				if err != nil {
					a.writeError(w, err)
					return true
				}
			}
			if limit < 1 || limit > 100 {
				a.writeError(w, fmt.Errorf("%w: 取得件数は1以上100以下で指定してください", service.ErrInvalid))
				return true
			}
			page, err := a.service.GetThreadMessages(r.Context(), userID, channel, root, storage.MessageQuery{BeforeSeq: before, AfterSeq: after, AroundSeq: around, Limit: int(limit)})
			if err != nil {
				a.writeError(w, err)
			} else {
				a.writeJSON(w, http.StatusOK, page)
			}
		case http.MethodPost:
			var input struct {
				Text string `json:"text"`
			}
			if !a.decode(w, r, &input) {
				return true
			}
			m, err := a.service.PostThreadMessage(r.Context(), userID, channel, root, input.Text)
			if err != nil {
				a.writeError(w, err)
			} else {
				a.writeJSON(w, http.StatusCreated, m)
			}
		default:
			a.methodNotAllowed(w, http.MethodGet, http.MethodPost)
		}
	case "read":
		if r.Method != http.MethodPut {
			a.methodNotAllowed(w, http.MethodPut)
			return true
		}
		var input struct {
			Seq int64 `json:"seq"`
		}
		if !a.decode(w, r, &input) {
			return true
		}
		if err := a.service.MarkThreadRead(r.Context(), userID, channel, root, input.Seq); err != nil {
			a.writeError(w, err)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		return false
	}
	return true
}
func (a *API) listThreads(w http.ResponseWriter, r *http.Request, userID, channel string) {
	if r.Method != http.MethodGet {
		a.methodNotAllowed(w, http.MethodGet)
		return
	}
	limit, err := queryInt(r, "limit", 50)
	if err != nil {
		a.writeError(w, err)
		return
	}
	if limit < 1 || limit > 100 {
		a.writeError(w, fmt.Errorf("%w: 取得件数は1以上100以下で指定してください", service.ErrInvalid))
		return
	}
	flags := map[string]bool{}
	for _, key := range []string{"participating", "unread"} {
		if value := r.URL.Query().Get(key); value != "" {
			v, e := strconv.ParseBool(value)
			if e != nil {
				a.writeError(w, fmt.Errorf("%w: 絞り込み条件が正しくありません", service.ErrInvalid))
				return
			}
			flags[key] = v
		}
	}
	page, err := a.service.ListThreads(r.Context(), userID, channel, flags["participating"], flags["unread"], r.URL.Query().Get("cursor"), int(limit))
	if err != nil {
		a.writeError(w, err)
	} else {
		a.writeJSON(w, http.StatusOK, page)
	}
}
