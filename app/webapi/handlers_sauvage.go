package webapi

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-pkgz/rest"

	"github.com/redstone-md/shield/app/community"
	"github.com/redstone-md/shield/app/storage"
)

type sauvageOverviewView struct {
	Snapshot        community.DashboardSnapshot
	ActionSummary   storage.ModerationActionSummary
	IncidentSummary storage.IncidentDashboardSummary
	Actions         []storage.ModerationActionEntry
	Settings        Settings
	Days            int
}

type sauvageActivityView struct {
	Events   []community.RuleEvent
	Actions  []storage.ModerationActionEntry
	Settings Settings
	Filter   community.RuleEventFilter
	Days     int
}

func (s *Server) htmlSauvageOverviewHandler(w http.ResponseWriter, r *http.Request) {
	days := boundedQueryInt(r, "days", 7, 1, 90)
	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	snapshot, err := s.CommunityDashboard.Dashboard(r.Context(), since, 50)
	if err != nil {
		http.Error(w, "No se pudo cargar el panel de Sauvage", http.StatusInternalServerError)
		return
	}
	actions, err := s.ModerationActions.Recent(r.Context(), since, 30)
	if err != nil {
		http.Error(w, "No se pudo cargar el diario de acciones", http.StatusInternalServerError)
		return
	}
	actionSummary, err := s.ModerationActions.Summary(r.Context(), since)
	if err != nil {
		http.Error(w, "No se pudo resumir el diario de acciones", http.StatusInternalServerError)
		return
	}
	incidentSummary, err := s.IncidentDashboard.DashboardSummary(r.Context(), since)
	if err != nil {
		http.Error(w, "No se pudieron resumir los casos", http.StatusInternalServerError)
		return
	}
	if err = tmpl.ExecuteTemplate(w, "sauvage.html", sauvageOverviewView{
		Snapshot: snapshot, ActionSummary: actionSummary, IncidentSummary: incidentSummary,
		Actions: actions, Settings: s.Settings, Days: days,
	}); err != nil {
		http.Error(w, "No se pudo renderizar el panel", http.StatusInternalServerError)
	}
}

func (s *Server) htmlSauvageActivityHandler(w http.ResponseWriter, r *http.Request) {
	days := boundedQueryInt(r, "days", 7, 1, 90)
	filter := community.RuleEventFilter{
		Since:    time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour),
		ThreadID: boundedQueryInt(r, "thread", 0, 0, 1_000_000),
		UserID:   queryInt64(r, "user"),
		RuleCode: strings.TrimSpace(r.URL.Query().Get("rule")),
		Action:   strings.TrimSpace(r.URL.Query().Get("action")),
		Limit:    boundedQueryInt(r, "limit", 200, 1, 1000),
	}
	switch r.URL.Query().Get("mode") {
	case "shadow":
		value := true
		filter.Shadow = &value
	case "live":
		value := false
		filter.Shadow = &value
	}
	events, err := s.CommunityDashboard.ListRuleEvents(r.Context(), filter)
	if err != nil {
		http.Error(w, "No se pudo cargar la actividad", http.StatusInternalServerError)
		return
	}
	actions, err := s.ModerationActions.Recent(r.Context(), filter.Since, filter.Limit)
	if err != nil {
		http.Error(w, "No se pudo cargar el diario de acciones", http.StatusInternalServerError)
		return
	}
	if err = tmpl.ExecuteTemplate(w, "sauvage_activity.html", sauvageActivityView{
		Events: events, Actions: actions, Settings: s.Settings, Filter: filter, Days: days,
	}); err != nil {
		http.Error(w, "No se pudo renderizar la actividad", http.StatusInternalServerError)
	}
}

func (s *Server) htmlSauvagePresentationsHandler(w http.ResponseWriter, r *http.Request) {
	records, err := s.CommunityDashboard.ListPresentations(r.Context(), boundedQueryInt(r, "limit", 500, 1, 1000))
	if err != nil {
		http.Error(w, "No se pudieron cargar las presentaciones", http.StatusInternalServerError)
		return
	}
	data := struct {
		Records  []community.PresentationRecord
		Settings Settings
	}{records, s.Settings}
	if err = tmpl.ExecuteTemplate(w, "sauvage_presentations.html", data); err != nil {
		http.Error(w, "No se pudo renderizar la página", http.StatusInternalServerError)
	}
}

func (s *Server) htmlSauvageContestsHandler(w http.ResponseWriter, r *http.Request) {
	contestID := strings.TrimSpace(r.URL.Query().Get("contest"))
	records, err := s.CommunityDashboard.ListContestEntries(
		r.Context(), contestID, boundedQueryInt(r, "limit", 500, 1, 1000),
	)
	if err != nil {
		http.Error(w, "No se pudieron cargar las participaciones", http.StatusInternalServerError)
		return
	}
	data := struct {
		Records   []community.ContestEntryRecord
		Settings  Settings
		ContestID string
	}{records, s.Settings, contestID}
	if err = tmpl.ExecuteTemplate(w, "sauvage_contests.html", data); err != nil {
		http.Error(w, "No se pudo renderizar la página", http.StatusInternalServerError)
	}
}

func (s *Server) htmlSauvageUsersHandler(w http.ResponseWriter, r *http.Request) {
	records, err := s.CommunityDashboard.ListViolations(r.Context(), boundedQueryInt(r, "limit", 500, 1, 1000))
	if err != nil {
		http.Error(w, "No se pudieron cargar las reincidencias", http.StatusInternalServerError)
		return
	}
	data := struct {
		Records  []community.ViolationRecord
		Settings Settings
	}{records, s.Settings}
	if err = tmpl.ExecuteTemplate(w, "sauvage_users.html", data); err != nil {
		http.Error(w, "No se pudo renderizar la página", http.StatusInternalServerError)
	}
}

func (s *Server) htmlSauvageSystemHandler(w http.ResponseWriter, _ *http.Request) {
	data := struct {
		Settings Settings
		Version  string
		Uptime   time.Duration
	}{s.Settings, s.Version, time.Since(startTime)}
	if err := tmpl.ExecuteTemplate(w, "sauvage_system.html", data); err != nil {
		http.Error(w, "No se pudo renderizar la página", http.StatusInternalServerError)
	}
}

func (s *Server) sauvageSummaryAPIHandler(w http.ResponseWriter, r *http.Request) {
	days := boundedQueryInt(r, "days", 7, 1, 90)
	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	snapshot, err := s.CommunityDashboard.Dashboard(r.Context(), since, 100)
	if err != nil {
		_ = rest.EncodeJSON(w, http.StatusInternalServerError, rest.JSON{"error": err.Error()})
		return
	}
	actions, err := s.ModerationActions.Summary(r.Context(), since)
	if err != nil {
		_ = rest.EncodeJSON(w, http.StatusInternalServerError, rest.JSON{"error": err.Error()})
		return
	}
	incidents, err := s.IncidentDashboard.DashboardSummary(r.Context(), since)
	if err != nil {
		_ = rest.EncodeJSON(w, http.StatusInternalServerError, rest.JSON{"error": err.Error()})
		return
	}
	_ = rest.EncodeJSON(w, http.StatusOK, map[string]any{
		"community": snapshot,
		"actions":   actions,
		"incidents": incidents,
		"settings":  newSauvagePublicSettings(s.Settings),
	})
}

type sauvagePublicSettings struct {
	TenantID              string `json:"tenant_id"`
	BotUsername           string `json:"bot_username"`
	PrimaryGroup          string `json:"primary_group"`
	DryRun                bool   `json:"dry_run"`
	CommunityApplyActions bool   `json:"community_apply_actions"`
	PresentationThreadID  int    `json:"presentation_thread_id"`
	ContestThreadID       int    `json:"contest_thread_id"`
	ContestID             string `json:"contest_id"`
	Model                 string `json:"model"`
}

func newSauvagePublicSettings(settings Settings) sauvagePublicSettings {
	return sauvagePublicSettings{
		TenantID: settings.TenantID, BotUsername: settings.BotUsername, PrimaryGroup: settings.PrimaryGroup,
		DryRun: settings.DryModeEnabled, CommunityApplyActions: settings.CommunityApplyActions,
		PresentationThreadID: settings.PresentationThreadID, ContestThreadID: settings.ContestThreadID,
		ContestID: settings.ContestID, Model: settings.OpenAIModel,
	}
}

func (s *Server) sauvageEventsAPIHandler(w http.ResponseWriter, r *http.Request) {
	days := boundedQueryInt(r, "days", 7, 1, 90)
	events, err := s.CommunityDashboard.ListRuleEvents(r.Context(), community.RuleEventFilter{
		Since: time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour),
		Limit: boundedQueryInt(r, "limit", 200, 1, 1000),
	})
	if err != nil {
		_ = rest.EncodeJSON(w, http.StatusInternalServerError, rest.JSON{"error": err.Error()})
		return
	}
	_ = rest.EncodeJSON(w, http.StatusOK, events)
}

func (s *Server) sauvageActionsAPIHandler(w http.ResponseWriter, r *http.Request) {
	days := boundedQueryInt(r, "days", 7, 1, 90)
	actions, err := s.ModerationActions.Recent(
		r.Context(), time.Now().UTC().Add(-time.Duration(days)*24*time.Hour),
		boundedQueryInt(r, "limit", 200, 1, 1000),
	)
	if err != nil {
		_ = rest.EncodeJSON(w, http.StatusInternalServerError, rest.JSON{"error": err.Error()})
		return
	}
	_ = rest.EncodeJSON(w, http.StatusOK, actions)
}

func (s *Server) sauvageCSVHandler(w http.ResponseWriter, r *http.Request) {
	days := boundedQueryInt(r, "days", 30, 1, 365)
	events, err := s.CommunityDashboard.ListRuleEvents(r.Context(), community.RuleEventFilter{
		Since: time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour),
		Limit: 1000,
	})
	if err != nil {
		http.Error(w, "No se pudo generar la exportación", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="sauvage-rule-events.csv"`)
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{
		"created_at", "topic", "user_id", "message_id", "rule",
		"action", "mode", "reason", "duration_seconds",
	})
	for _, event := range events {
		mode := "live"
		if event.Shadow {
			mode = "shadow"
		}
		_ = writer.Write([]string{
			event.CreatedAt.UTC().Format(time.RFC3339), topicName(event.ThreadID), strconv.FormatInt(event.UserID, 10),
			strconv.Itoa(event.MessageID), event.RuleCode, event.Action, mode, event.Reason,
			strconv.FormatInt(event.DurationSecond, 10),
		})
	}
	writer.Flush()
}

func boundedQueryInt(r *http.Request, name string, fallback, minValue, maxValue int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return fallback
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func queryInt64(r *http.Request, name string) int64 {
	value, _ := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	return value
}

func topicName(threadID int) string {
	switch threadID {
	case 1:
		return "General"
	case 2:
		return "Preguntas"
	case 3:
		return "Presentaciones"
	case 5:
		return "Calendario"
	case 6:
		return "Concurso"
	default:
		return fmt.Sprintf("Topic %d", threadID)
	}
}

func telegramMessageURL(chatID int64, threadID, messageID int) string {
	chat := strings.TrimPrefix(strconv.FormatInt(chatID, 10), "-100")
	if chat == "" || messageID <= 0 {
		return ""
	}
	if threadID > 0 {
		return fmt.Sprintf("https://t.me/c/%s/%d/%d", chat, threadID, messageID)
	}
	return fmt.Sprintf("https://t.me/c/%s/%d", chat, messageID)
}
