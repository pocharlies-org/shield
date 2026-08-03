package community

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DashboardSummary contains the operational totals shown in the Sauvage dashboard.
type DashboardSummary struct {
	Since            time.Time
	TotalEvents      int
	Allowed          int
	Violations       int
	ShadowViolations int
	LiveViolations   int
	Warnings         int
	Restrictions     int
	Bans             int
	Presentations    int
	ContestEntries   int
	UsersWithStrikes int
}

// DailyRuleCount groups community decisions by UTC day and enforcement mode.
type DailyRuleCount struct {
	Day    string `db:"day"`
	Shadow bool   `db:"shadow"`
	Count  int    `db:"count"`
}

// RuleEvent is one deterministic topic decision.
type RuleEvent struct {
	EventKey         string    `db:"event_key"`
	ChatID           int64     `db:"chat_id"`
	ThreadID         int       `db:"thread_id"`
	MessageID        int       `db:"message_id"`
	RelatedMessageID int       `db:"related_message_id"`
	MediaGroupID     string    `db:"media_group_id" json:"-"`
	UserID           int64     `db:"user_id"`
	UserName         string    `db:"username"`
	DisplayName      string    `db:"display_name"`
	RuleCode         string    `db:"rule_code"`
	Action           string    `db:"action"`
	Reason           string    `db:"reason"`
	MessageText      string    `db:"message_text" json:"-"`
	UserMessage      string    `db:"user_message"`
	DurationSecond   int64     `db:"duration_seconds"`
	GroupSize        int       `db:"group_size" json:"-"`
	Shadow           bool      `db:"shadow"`
	CreatedAt        time.Time `db:"created_at"`
}

// PresentationRecord identifies a member's persistent presentation claim.
type PresentationRecord struct {
	ChatID         int64     `db:"chat_id"`
	UserID         int64     `db:"user_id"`
	UserName       string    `db:"username"`
	DisplayName    string    `db:"display_name"`
	ThreadID       int       `db:"thread_id"`
	FirstMessageID int       `db:"first_message_id"`
	EntryKey       string    `db:"entry_key"`
	CreatedAt      time.Time `db:"created_at"`
	MessageText    string    `db:"message_text"`
}

// PresentationFilter bounds and filters registered presentation claims.
type PresentationFilter struct {
	UserQuery    string
	MessageQuery string
	Limit        int
}

// ContestEntryRecord identifies one member entry in one contest.
type ContestEntryRecord struct {
	ContestID      string    `db:"contest_id"`
	ChatID         int64     `db:"chat_id"`
	UserID         int64     `db:"user_id"`
	UserName       string    `db:"username"`
	DisplayName    string    `db:"display_name"`
	ThreadID       int       `db:"thread_id"`
	FirstMessageID int       `db:"first_message_id"`
	EntryKey       string    `db:"entry_key"`
	CreatedAt      time.Time `db:"created_at"`
}

// ViolationRecord contains the live strike counter for one member and rule.
type ViolationRecord struct {
	UserID      int64     `db:"user_id"`
	UserName    string    `db:"username"`
	DisplayName string    `db:"display_name"`
	RuleCode    string    `db:"rule_code"`
	Strikes     int       `db:"strikes"`
	UpdatedAt   time.Time `db:"updated_at"`
}

// DashboardSnapshot is a bounded operational view for the web UI and API.
type DashboardSnapshot struct {
	Summary      DashboardSummary
	Daily        []DailyRuleCount
	RecentEvents []RuleEvent
}

// RuleEventFilter bounds and filters a community event query.
type RuleEventFilter struct {
	Since        time.Time
	ThreadID     int
	UserID       int64
	UserQuery    string
	MessageQuery string
	RuleCode     string
	Action       string
	Shadow       *bool
	Limit        int
}

// Dashboard returns summary totals, a daily trend, and recent decisions.
func (s *Store) Dashboard(ctx context.Context, since time.Time, limit int) (DashboardSnapshot, error) {
	if since.IsZero() {
		since = time.Now().UTC().Add(-7 * 24 * time.Hour)
	}
	events, err := s.ListRuleEvents(ctx, RuleEventFilter{Since: since, Limit: limit})
	if err != nil {
		return DashboardSnapshot{}, err
	}

	summary := DashboardSummary{Since: since}
	groupExpr := logicalRuleEventGroup("e")
	query := s.db.Adopt(`WITH ranked_events AS (
		SELECT e.action, e.shadow,
			ROW_NUMBER() OVER (
				PARTITION BY ` + groupExpr + `
				ORDER BY e.created_at ASC, e.message_id ASC
			) AS group_rank
		FROM community_rule_events e
		WHERE e.tenant_id = ? AND e.created_at >= ?
	)
	SELECT
		COUNT(*) AS total_events,
		COALESCE(SUM(CASE WHEN action = 'allow' THEN 1 ELSE 0 END), 0) AS allowed,
		COALESCE(SUM(CASE WHEN action <> 'allow' THEN 1 ELSE 0 END), 0) AS violations,
		COALESCE(SUM(CASE WHEN action <> 'allow' AND shadow = ? THEN 1 ELSE 0 END), 0) AS shadow_violations,
		COALESCE(SUM(CASE WHEN action <> 'allow' AND shadow = ? THEN 1 ELSE 0 END), 0) AS live_violations,
		COALESCE(SUM(CASE WHEN action = 'warn' THEN 1 ELSE 0 END), 0) AS warnings,
		COALESCE(SUM(CASE WHEN action = 'restrict' THEN 1 ELSE 0 END), 0) AS restrictions,
		COALESCE(SUM(CASE WHEN action = 'ban' THEN 1 ELSE 0 END), 0) AS bans
		FROM ranked_events WHERE group_rank = 1`)
	var totals struct {
		TotalEvents      int `db:"total_events"`
		Allowed          int `db:"allowed"`
		Violations       int `db:"violations"`
		ShadowViolations int `db:"shadow_violations"`
		LiveViolations   int `db:"live_violations"`
		Warnings         int `db:"warnings"`
		Restrictions     int `db:"restrictions"`
		Bans             int `db:"bans"`
	}
	if err = s.db.GetContext(ctx, &totals, query, s.db.TenantID(), since.UTC(), true, false); err != nil {
		return DashboardSnapshot{}, fmt.Errorf("load community dashboard totals: %w", err)
	}
	summary.TotalEvents = totals.TotalEvents
	summary.Allowed = totals.Allowed
	summary.Violations = totals.Violations
	summary.ShadowViolations = totals.ShadowViolations
	summary.LiveViolations = totals.LiveViolations
	summary.Warnings = totals.Warnings
	summary.Restrictions = totals.Restrictions
	summary.Bans = totals.Bans

	if err = s.db.GetContext(ctx, &summary.Presentations, s.db.Adopt(
		`SELECT COUNT(*) FROM community_presentations WHERE tenant_id = ?`), s.db.TenantID()); err != nil {
		return DashboardSnapshot{}, fmt.Errorf("count community presentations: %w", err)
	}
	if err = s.db.GetContext(ctx, &summary.ContestEntries, s.db.Adopt(
		`SELECT COUNT(*) FROM community_contest_entries WHERE tenant_id = ?`), s.db.TenantID()); err != nil {
		return DashboardSnapshot{}, fmt.Errorf("count community contest entries: %w", err)
	}
	if err = s.db.GetContext(ctx, &summary.UsersWithStrikes, s.db.Adopt(
		`SELECT COUNT(DISTINCT user_id) FROM community_violations WHERE tenant_id = ?`), s.db.TenantID()); err != nil {
		return DashboardSnapshot{}, fmt.Errorf("count community users with strikes: %w", err)
	}

	var daily []DailyRuleCount
	dailyQuery := s.db.Adopt(`WITH ranked_events AS (
		SELECT e.action, e.shadow, e.created_at,
			ROW_NUMBER() OVER (
				PARTITION BY ` + groupExpr + `
				ORDER BY e.created_at ASC, e.message_id ASC
			) AS group_rank
		FROM community_rule_events e
		WHERE e.tenant_id = ? AND e.created_at >= ?
	)
	SELECT SUBSTR(CAST(created_at AS TEXT), 1, 10) AS day, shadow, COUNT(*) AS count
		FROM ranked_events
		WHERE group_rank = 1 AND action <> 'allow'
		GROUP BY SUBSTR(CAST(created_at AS TEXT), 1, 10), shadow
		ORDER BY SUBSTR(CAST(created_at AS TEXT), 1, 10) ASC, shadow DESC`)
	if err = s.db.SelectContext(ctx, &daily, dailyQuery, s.db.TenantID(), since.UTC()); err != nil {
		return DashboardSnapshot{}, fmt.Errorf("load community daily trend: %w", err)
	}
	return DashboardSnapshot{Summary: summary, Daily: daily, RecentEvents: events}, nil
}

// ListRuleEvents returns newest decisions first.
func (s *Store) ListRuleEvents(ctx context.Context, filter RuleEventFilter) ([]RuleEvent, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	where := []string{"e.tenant_id = ?", "e.group_rank = 1"}
	args := []any{s.db.TenantID()}
	if !filter.Since.IsZero() {
		where = append(where, "e.created_at >= ?")
		args = append(args, filter.Since.UTC())
	}
	if filter.ThreadID > 0 {
		where = append(where, "e.thread_id = ?")
		args = append(args, filter.ThreadID)
	}
	if filter.UserID != 0 {
		where = append(where, "e.user_id = ?")
		args = append(args, filter.UserID)
	}
	if query := normalizedSearch(filter.UserQuery); query != "" {
		where = append(where, `(LOWER(e.username) LIKE ? OR LOWER(e.display_name) LIKE ?)`)
		args = append(args, query, query)
	}
	if query := normalizedSearch(filter.MessageQuery); query != "" {
		where = append(where, `(LOWER(e.message_text) LIKE ? OR CAST(e.message_id AS TEXT) LIKE ? OR CAST(e.related_message_id AS TEXT) LIKE ?)`)
		args = append(args, query, query, query)
	}
	if strings.TrimSpace(filter.RuleCode) != "" {
		where = append(where, "e.rule_code = ?")
		args = append(args, strings.TrimSpace(filter.RuleCode))
	}
	if strings.TrimSpace(filter.Action) != "" {
		where = append(where, "e.action = ?")
		args = append(args, strings.TrimSpace(filter.Action))
	}
	if filter.Shadow != nil {
		where = append(where, "e.shadow = ?")
		args = append(args, *filter.Shadow)
	}
	args = append(args, limit)

	groupExpr := logicalRuleEventGroup("source")
	query := s.db.Adopt(`WITH ranked_events AS (
		SELECT source.tenant_id, source.event_key, source.chat_id, source.thread_id, source.message_id,
			source.related_message_id, source.media_group_id, source.user_id,
			COALESCE(m.username, '') AS username, COALESCE(m.display_name, '') AS display_name,
			source.rule_code, source.action, source.reason, source.message_text, source.user_message,
			source.duration_seconds, source.shadow, source.created_at,
			ROW_NUMBER() OVER (
				PARTITION BY ` + groupExpr + `
				ORDER BY source.created_at ASC, source.message_id ASC
			) AS group_rank,
			COUNT(*) OVER (PARTITION BY ` + groupExpr + `) AS group_size
		FROM community_rule_events source
		LEFT JOIN community_members m
		  ON m.tenant_id = source.tenant_id AND m.chat_id = source.chat_id AND m.user_id = source.user_id
	)
	SELECT e.event_key, e.chat_id, e.thread_id, e.message_id, e.related_message_id,
		e.media_group_id, e.user_id,
		e.username, e.display_name, e.rule_code, e.action, e.reason, e.message_text, e.user_message,
		e.duration_seconds, e.group_size, e.shadow, e.created_at
		FROM ranked_events e
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY e.created_at DESC, e.message_id DESC LIMIT ?`)
	var events []RuleEvent
	if err := s.db.SelectContext(ctx, &events, query, args...); err != nil {
		return nil, fmt.Errorf("list community rule events: %w", err)
	}
	return events, nil
}

func logicalRuleEventGroup(alias string) string {
	return `CASE
		WHEN ` + alias + `.media_group_id <> ''
		THEN 'tenant:' || ` + alias + `.tenant_id || ':album:' ||
			CAST(` + alias + `.chat_id AS TEXT) || ':' || ` + alias + `.media_group_id
		ELSE 'tenant:' || ` + alias + `.tenant_id || ':event:' || ` + alias + `.event_key
	END`
}

// ListPresentations returns persistent presentation claims newest first.
func (s *Store) ListPresentations(ctx context.Context, filter PresentationFilter) ([]PresentationRecord, error) {
	limit := boundedLimit(filter.Limit)
	where := []string{"p.tenant_id = ?"}
	args := []any{s.db.TenantID()}
	if query := normalizedSearch(filter.UserQuery); query != "" {
		where = append(where, `(LOWER(COALESCE(m.username, '')) LIKE ? OR LOWER(COALESCE(m.display_name, '')) LIKE ?)`)
		args = append(args, query, query)
	}
	if query := normalizedSearch(filter.MessageQuery); query != "" {
		where = append(where, `(LOWER(COALESCE(e.message_text, '')) LIKE ? OR CAST(p.first_message_id AS TEXT) LIKE ?)`)
		args = append(args, query, query)
	}
	args = append(args, limit)
	query := s.db.Adopt(`SELECT p.chat_id, p.user_id, COALESCE(m.username, '') AS username,
		COALESCE(m.display_name, '') AS display_name, p.thread_id, p.first_message_id, p.entry_key, p.created_at,
		COALESCE(e.message_text, '') AS message_text
		FROM community_presentations p
		LEFT JOIN community_members m
		  ON m.tenant_id = p.tenant_id AND m.chat_id = p.chat_id AND m.user_id = p.user_id
		LEFT JOIN community_rule_events e
		  ON e.tenant_id = p.tenant_id AND e.chat_id = p.chat_id AND e.message_id = p.first_message_id
		WHERE ` + strings.Join(where, " AND ") + ` ORDER BY p.created_at DESC LIMIT ?`)
	var records []PresentationRecord
	if err := s.db.SelectContext(ctx, &records, query, args...); err != nil {
		return nil, fmt.Errorf("list community presentations: %w", err)
	}
	return records, nil
}

func normalizedSearch(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, "@")))
	if value == "" {
		return ""
	}
	return "%" + value + "%"
}

// ListContestEntries returns contest claims newest first, optionally for one contest.
func (s *Store) ListContestEntries(ctx context.Context, contestID string, limit int) ([]ContestEntryRecord, error) {
	limit = boundedLimit(limit)
	where := "tenant_id = ?"
	args := []any{s.db.TenantID()}
	if strings.TrimSpace(contestID) != "" {
		where += " AND contest_id = ?"
		args = append(args, strings.TrimSpace(contestID))
	}
	args = append(args, limit)
	query := s.db.Adopt(`SELECT c.contest_id, c.chat_id, c.user_id, COALESCE(m.username, '') AS username,
		COALESCE(m.display_name, '') AS display_name, c.thread_id, c.first_message_id, c.entry_key, c.created_at
		FROM community_contest_entries c
		LEFT JOIN community_members m
		  ON m.tenant_id = c.tenant_id AND m.chat_id = c.chat_id AND m.user_id = c.user_id
		WHERE ` + strings.ReplaceAll(where, "tenant_id", "c.tenant_id") + ` ORDER BY c.created_at DESC LIMIT ?`)
	var records []ContestEntryRecord
	if err := s.db.SelectContext(ctx, &records, query, args...); err != nil {
		return nil, fmt.Errorf("list community contest entries: %w", err)
	}
	return records, nil
}

// ListViolations returns current live strike counters.
func (s *Store) ListViolations(ctx context.Context, limit int) ([]ViolationRecord, error) {
	limit = boundedLimit(limit)
	query := s.db.Adopt(`SELECT v.user_id, COALESCE(m.username, '') AS username,
		COALESCE(m.display_name, '') AS display_name, v.rule_code, v.strikes, v.updated_at
		FROM community_violations v
		LEFT JOIN community_members m
		  ON m.tenant_id = v.tenant_id AND m.chat_id = v.chat_id AND m.user_id = v.user_id
		WHERE v.tenant_id = ?
		ORDER BY v.strikes DESC, v.updated_at DESC LIMIT ?`)
	var records []ViolationRecord
	if err := s.db.SelectContext(ctx, &records, query, s.db.TenantID(), limit); err != nil {
		return nil, fmt.Errorf("list community violations: %w", err)
	}
	return records, nil
}

func boundedLimit(limit int) int {
	if limit <= 0 || limit > 1000 {
		return 200
	}
	return limit
}
