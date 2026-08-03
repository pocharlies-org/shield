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
	IncomingSummary storage.IncomingEventSummary
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
	Mode     string
}

type sauvagePresentationsView struct {
	Records  []community.PresentationRecord
	Settings Settings
	Filter   community.PresentationFilter
}

type sauvagePresentationUserSuggestion struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	UserID      int64  `json:"user_id"`
	UserName    string `json:"username,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

func (s *Server) htmlSauvageOverviewHandler(w http.ResponseWriter, r *http.Request) {
	days := boundedQueryInt(r, "days", 7, 1, 90)
	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	snapshot, err := s.CommunityDashboard.Dashboard(r.Context(), since, 50)
	if err != nil {
		http.Error(w, "No se pudo cargar el panel de Sauvage", http.StatusInternalServerError)
		return
	}
	incomingSummary, err := s.IncomingEvents.Summary(r.Context(), since)
	if err != nil {
		http.Error(w, "No se pudieron resumir los mensajes analizados", http.StatusInternalServerError)
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
		Snapshot: snapshot, IncomingSummary: incomingSummary,
		ActionSummary: actionSummary, IncidentSummary: incidentSummary,
		Actions: actions, Settings: s.Settings, Days: days,
	}); err != nil {
		http.Error(w, "No se pudo renderizar el panel", http.StatusInternalServerError)
	}
}

func (s *Server) htmlSauvageActivityHandler(w http.ResponseWriter, r *http.Request) {
	days := boundedQueryInt(r, "days", 7, 1, 90)
	mode := strings.TrimSpace(r.URL.Query().Get("mode"))
	userQuery := strings.TrimSpace(r.URL.Query().Get("user"))
	userID := queryInt64(r, "user")
	if userID != 0 {
		userQuery = ""
	}
	filter := community.RuleEventFilter{
		Since:        time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour),
		ThreadID:     boundedQueryInt(r, "thread", 0, 0, 1_000_000),
		UserID:       userID,
		UserQuery:    userQuery,
		MessageQuery: strings.TrimSpace(r.URL.Query().Get("message")),
		RuleCode:     strings.TrimSpace(r.URL.Query().Get("rule")),
		Action:       strings.TrimSpace(r.URL.Query().Get("action")),
		Limit:        boundedQueryInt(r, "limit", 200, 1, 1000),
	}
	switch mode {
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
	actions, err := s.ModerationActions.RecentDetailed(r.Context(), filter.Since, filter.Limit)
	if err != nil {
		http.Error(w, "No se pudo cargar el diario de acciones", http.StatusInternalServerError)
		return
	}
	if err = tmpl.ExecuteTemplate(w, "sauvage_activity.html", sauvageActivityView{
		Events: events, Actions: actions, Settings: s.Settings, Filter: filter, Days: days, Mode: mode,
	}); err != nil {
		http.Error(w, "No se pudo renderizar la actividad", http.StatusInternalServerError)
	}
}

func (s *Server) htmlSauvagePresentationsHandler(w http.ResponseWriter, r *http.Request) {
	filter := community.PresentationFilter{
		UserQuery:    strings.TrimSpace(r.URL.Query().Get("user")),
		MessageQuery: strings.TrimSpace(r.URL.Query().Get("message")),
		Limit:        boundedQueryInt(r, "limit", 500, 1, 1000),
	}
	records, err := s.CommunityDashboard.ListPresentations(r.Context(), filter)
	if err != nil {
		http.Error(w, "No se pudieron cargar las presentaciones", http.StatusInternalServerError)
		return
	}
	data := sauvagePresentationsView{
		Records: records, Settings: s.Settings, Filter: filter,
	}
	if err = tmpl.ExecuteTemplate(w, "sauvage_presentations.html", data); err != nil {
		http.Error(w, "No se pudo renderizar la página", http.StatusInternalServerError)
	}
}

func (s *Server) sauvagePresentationUserSuggestionsHandler(w http.ResponseWriter, r *http.Request) {
	records, err := s.CommunityDashboard.ListPresentations(r.Context(), community.PresentationFilter{
		UserQuery: strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:     20,
	})
	if err != nil {
		http.Error(w, "No se pudieron buscar usuarios de presentaciones", http.StatusInternalServerError)
		return
	}
	suggestions := make([]sauvagePresentationUserSuggestion, 0, len(records))
	for _, record := range records {
		suggestions = append(suggestions, newSauvagePresentationUserSuggestion(record))
	}
	rest.RenderJSON(w, suggestions)
}

func newSauvagePresentationUserSuggestion(record community.PresentationRecord) sauvagePresentationUserSuggestion {
	userName := strings.TrimPrefix(strings.TrimSpace(record.UserName), "@")
	displayName := strings.TrimSpace(record.DisplayName)
	value := displayName
	if userName != "" {
		value = "@" + userName
	}
	if value == "" {
		value = strconv.FormatInt(record.UserID, 10)
	}

	identity := value
	if displayName != "" && userName != "" {
		identity = fmt.Sprintf("%s (@%s)", displayName, userName)
	}
	return sauvagePresentationUserSuggestion{
		Value: value, Label: fmt.Sprintf("%s · ID %d", identity, record.UserID), UserID: record.UserID,
		UserName: userName, DisplayName: displayName,
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
	var contests []community.Contest
	var selected community.Contest
	var leaderboard []community.ContestLeaderboardEntry
	var appeals []community.ContestAppeal
	if s.ContestManager != nil {
		contests, err = s.ContestManager.List(r.Context(), 100)
		if err != nil {
			http.Error(w, "No se pudieron cargar los concursos", http.StatusInternalServerError)
			return
		}
		if contestID == "" && len(contests) > 0 {
			contestID = contests[0].ContestID
		}
		if contestID != "" {
			selected, err = s.ContestManager.Get(r.Context(), contestID)
			if err != nil {
				http.Error(w, "No se pudo cargar el concurso", http.StatusInternalServerError)
				return
			}
			leaderboard, err = s.ContestManager.Leaderboard(r.Context(), contestID)
			if err != nil {
				http.Error(w, "No se pudo obtener la clasificación", http.StatusInternalServerError)
				return
			}
			appeals, err = s.ContestManager.Appeals(r.Context(), contestID, 200)
			if err != nil {
				http.Error(w, "No se pudieron cargar las apelaciones", http.StatusInternalServerError)
				return
			}
		}
	}
	data := struct {
		Records     []community.ContestEntryRecord
		Settings    Settings
		ContestID   string
		Contests    []community.Contest
		Selected    community.Contest
		Leaderboard []community.ContestLeaderboardEntry
		Appeals     []community.ContestAppeal
	}{records, s.Settings, contestID, contests, selected, leaderboard, appeals}
	if err = tmpl.ExecuteTemplate(w, "sauvage_contests.html", data); err != nil {
		http.Error(w, "No se pudo renderizar la página", http.StatusInternalServerError)
	}
}

func (s *Server) sauvageContestDraftHandler(w http.ResponseWriter, r *http.Request) {
	title, bases := strings.TrimSpace(r.FormValue("title")), strings.TrimSpace(r.FormValue("bases"))
	var deadline *time.Time
	if value := strings.TrimSpace(r.FormValue("deadline")); value != "" {
		parsed, err := time.ParseInLocation("2006-01-02T15:04", value, time.Local)
		if err != nil {
			http.Error(w, "Fecha límite no válida", http.StatusBadRequest)
			return
		}
		parsed = parsed.UTC()
		deadline = &parsed
	}
	user, _, _ := r.BasicAuth()
	contest, err := s.ContestManager.CreateDraft(r.Context(), community.ContestDraftInput{
		Title: title, Bases: bases, DeadlineAt: deadline, CreatedBy: user,
	})
	if err != nil {
		http.Error(w, "No se pudo crear el borrador: "+err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sauvage/contests?contest="+contest.ContestID, http.StatusSeeOther)
}

func (s *Server) sauvageContestPublishHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.ContestManager.UpdateDraft(r.Context(), id, r.FormValue("announcement")); err != nil {
		http.Error(w, "No se pudo guardar la vista previa: "+err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.ContestManager.Publish(r.Context(), id); err != nil {
		http.Error(w, "No se pudo publicar el concurso: "+err.Error(), http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, "/sauvage/contests?contest="+id, http.StatusSeeOther)
}

func (s *Server) sauvageContestBeginFinalizeHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.ContestManager.BeginFinalize(r.Context(), id); err != nil {
		http.Error(w, "No se pudo cerrar el concurso: "+err.Error(), http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, "/sauvage/contests?contest="+id, http.StatusSeeOther)
}

func (s *Server) sauvageContestConfirmFinalizeHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if strings.TrimSpace(r.FormValue("confirmation")) != "BORRAR" {
		http.Error(w, "Escribe BORRAR para confirmar", http.StatusBadRequest)
		return
	}
	if _, err := s.ContestManager.ConfirmFinalize(r.Context(), id); err != nil {
		http.Error(w, "No se pudo finalizar el concurso: "+err.Error(), http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, "/sauvage/contests?contest="+id, http.StatusSeeOther)
}

func (s *Server) sauvageContestAppealHandler(w http.ResponseWriter, r *http.Request) {
	accepted := r.FormValue("decision") == "accept"
	appeal, err := s.ContestManager.ResolveAppeal(r.Context(), r.PathValue("token"), accepted, r.FormValue("resolution"))
	if err != nil {
		http.Error(w, "No se pudo resolver la apelación: "+err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sauvage/contests?contest="+appeal.ContestID, http.StatusSeeOther)
}

func (s *Server) htmlSauvageUsersHandler(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	records, err := s.CommunityDashboard.ListMembers(r.Context(), community.MemberFilter{
		Query: query, Limit: boundedQueryInt(r, "limit", 200, 1, 1000),
	})
	if err != nil {
		http.Error(w, "No se pudo cargar el directorio de usuarios", http.StatusInternalServerError)
		return
	}
	reports, err := s.CommunityDashboard.ListUserReports(r.Context(), community.UserReportFilter{Limit: 100})
	if err != nil {
		http.Error(w, "No se pudo cargar la información de avisos", http.StatusInternalServerError)
		return
	}
	data := struct {
		Records  []community.MemberRecord
		Reports  []community.UserReportRecord
		Settings Settings
		Query    string
	}{records, reports, s.Settings, query}
	if err = tmpl.ExecuteTemplate(w, "sauvage_users.html", data); err != nil {
		http.Error(w, "No se pudo renderizar la página", http.StatusInternalServerError)
	}
}

func (s *Server) htmlSauvageUserDetailHandler(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || userID == 0 {
		http.Error(w, "Usuario no válido", http.StatusBadRequest)
		return
	}
	members, err := s.CommunityDashboard.ListMembers(r.Context(), community.MemberFilter{
		UserID: userID, Limit: 1,
	})
	if err != nil {
		http.Error(w, "No se pudo cargar el usuario", http.StatusInternalServerError)
		return
	}
	if len(members) == 0 {
		http.NotFound(w, r)
		return
	}
	reports, err := s.CommunityDashboard.ListUserReports(r.Context(), community.UserReportFilter{
		ChatID: members[0].ChatID, ReportedUserID: userID, Limit: 200,
	})
	if err != nil {
		http.Error(w, "No se pudo cargar la información del usuario", http.StatusInternalServerError)
		return
	}
	events, err := s.CommunityDashboard.ListRuleEvents(r.Context(), community.RuleEventFilter{
		UserID: userID, Limit: 200,
	})
	if err != nil {
		http.Error(w, "No se pudo cargar la actividad del usuario", http.StatusInternalServerError)
		return
	}
	data := struct {
		Member   community.MemberRecord
		Reports  []community.UserReportRecord
		Events   []community.RuleEvent
		Settings Settings
	}{members[0], reports, events, s.Settings}
	if err = tmpl.ExecuteTemplate(w, "sauvage_user_detail.html", data); err != nil {
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
	incoming, err := s.IncomingEvents.Summary(r.Context(), since)
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
		"incoming":  incoming,
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
