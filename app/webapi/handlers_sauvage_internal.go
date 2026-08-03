package webapi

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-pkgz/rest"

	"github.com/redstone-md/shield/app/community"
)

func (s *Server) sauvageInternalAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		expected := s.SauvageInternalToken
		if token == "" || len(token) != len(expected) || subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sauvageInternalOrAuth(fallback func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		protected := fallback(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/internal/sauvage/") && s.validSauvageInternalToken(r) {
				next.ServeHTTP(w, r)
				return
			}
			protected.ServeHTTP(w, r)
		})
	}
}

func (s *Server) validSauvageInternalToken(r *http.Request) bool {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	expected := s.SauvageInternalToken
	return token != "" && expected != "" && len(token) == len(expected) &&
		subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

func (s *Server) internalSauvageContestListHandler(w http.ResponseWriter, r *http.Request) {
	contests, err := s.ContestManager.List(r.Context(), boundedQueryInt(r, "limit", 100, 1, 500))
	if err != nil {
		internalContestError(w, err, http.StatusInternalServerError)
		return
	}
	_ = rest.EncodeJSON(w, http.StatusOK, map[string]any{"contests": contests})
}

func (s *Server) internalSauvageContestGetHandler(w http.ResponseWriter, r *http.Request) {
	contest, err := s.ContestManager.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		internalContestError(w, err, http.StatusNotFound)
		return
	}
	_ = rest.EncodeJSON(w, http.StatusOK, contest)
}

func (s *Server) internalSauvageContestLeaderboardHandler(w http.ResponseWriter, r *http.Request) {
	leaders, err := s.ContestManager.Leaderboard(r.Context(), r.PathValue("id"))
	if err != nil {
		internalContestError(w, err, http.StatusBadRequest)
		return
	}
	_ = rest.EncodeJSON(w, http.StatusOK, map[string]any{"leaderboard": leaders})
}

func (s *Server) internalSauvageContestCreateHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title      string `json:"title"`
		Bases      string `json:"bases"`
		DeadlineAt string `json:"deadline_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		internalContestError(w, err, http.StatusBadRequest)
		return
	}
	var deadline *time.Time
	if strings.TrimSpace(body.DeadlineAt) != "" {
		value, err := time.Parse(time.RFC3339, body.DeadlineAt)
		if err != nil {
			internalContestError(w, err, http.StatusBadRequest)
			return
		}
		value = value.UTC()
		deadline = &value
	}
	contest, err := s.ContestManager.CreateDraft(r.Context(), community.ContestDraftInput{
		Title: body.Title, Bases: body.Bases, DeadlineAt: deadline, CreatedBy: "agentgateway",
	})
	if err != nil {
		internalContestError(w, err, http.StatusBadRequest)
		return
	}
	_ = rest.EncodeJSON(w, http.StatusCreated, contest)
}

func (s *Server) internalSauvageContestPublishHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Announcement string `json:"announcement_text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		internalContestError(w, err, http.StatusBadRequest)
		return
	}
	if err := s.ContestManager.UpdateDraft(r.Context(), r.PathValue("id"), body.Announcement); err != nil {
		internalContestError(w, err, http.StatusBadRequest)
		return
	}
	contest, err := s.ContestManager.Publish(r.Context(), r.PathValue("id"))
	if err != nil {
		internalContestError(w, err, http.StatusBadGateway)
		return
	}
	_ = rest.EncodeJSON(w, http.StatusOK, contest)
}

func (s *Server) internalSauvageContestBeginFinalizeHandler(w http.ResponseWriter, r *http.Request) {
	contest, err := s.ContestManager.BeginFinalize(r.Context(), r.PathValue("id"))
	if err != nil {
		internalContestError(w, err, http.StatusBadGateway)
		return
	}
	_ = rest.EncodeJSON(w, http.StatusOK, contest)
}

func (s *Server) internalSauvageContestConfirmFinalizeHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirmation string `json:"confirmation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		internalContestError(w, err, http.StatusBadRequest)
		return
	}
	if body.Confirmation != "BORRAR" {
		internalContestError(w, errConfirmationRequired{}, http.StatusBadRequest)
		return
	}
	contest, err := s.ContestManager.ConfirmFinalize(r.Context(), r.PathValue("id"))
	if err != nil {
		internalContestError(w, err, http.StatusBadGateway)
		return
	}
	_ = rest.EncodeJSON(w, http.StatusOK, contest)
}

type errConfirmationRequired struct{}

func (errConfirmationRequired) Error() string { return "confirmation must be BORRAR" }

func internalContestError(w http.ResponseWriter, err error, status int) {
	_ = rest.EncodeJSON(w, status, map[string]string{"error": err.Error()})
}
