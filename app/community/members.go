package community

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redstone-md/shield/app/events"
)

// MemberRecord is the dashboard projection for one Sauvage member.
type MemberRecord struct {
	ChatID                int64        `db:"chat_id"`
	UserID                int64        `db:"user_id"`
	UserName              string       `db:"username"`
	DisplayName           string       `db:"display_name"`
	LastMessageID         int          `db:"last_message_id"`
	LastThreadID          int          `db:"last_thread_id"`
	LastMessageAt         time.Time    `db:"last_message_at"`
	HasPresentation       bool         `db:"has_presentation"`
	PresentationMessageID int          `db:"presentation_message_id"`
	PresentationThreadID  int          `db:"presentation_thread_id"`
	PresentationCreatedAt sql.NullTime `db:"presentation_created_at"`
	ReportCount           int          `db:"report_count"`
	WarningCount          int          `db:"warning_count"`
}

// MemberFilter bounds and filters the member directory.
type MemberFilter struct {
	ChatID int64
	Query  string
	UserID int64
	Limit  int
}

// UserReportRecord is one complaint collected by the private bot.
type UserReportRecord struct {
	ReportKey           string    `db:"report_key"`
	ChatID              int64     `db:"chat_id"`
	ReporterUserID      int64     `db:"reporter_user_id"`
	ReporterUserName    string    `db:"reporter_username"`
	ReporterDisplayName string    `db:"reporter_display_name"`
	ReportedUserID      int64     `db:"reported_user_id"`
	ReportedUserName    string    `db:"reported_username"`
	ReportedDisplayName string    `db:"reported_display_name"`
	SourceMessageID     int       `db:"source_message_id"`
	SourceThreadID      int       `db:"source_thread_id"`
	Reason              string    `db:"reason"`
	Status              string    `db:"status"`
	CreatedAt           time.Time `db:"created_at"`
}

// UserReportFilter bounds report queries.
type UserReportFilter struct {
	ChatID         int64
	ReportedUserID int64
	Status         string
	Limit          int
}

func (s *Store) observeMember(ctx context.Context, msg events.CommunityMessage) error {
	return s.ObserveMember(ctx, events.CommunityMember{
		ChatID: msg.ChatID, UserID: msg.UserID, UserName: msg.UserName, DisplayName: msg.DisplayName,
		LastMessageID: msg.MessageID, LastThreadID: msg.ThreadID, LastMessageAt: timestamp(msg.ReceivedAt),
	})
}

// ObserveMember upserts identity and advances last-message metadata only for newer observations.
func (s *Store) ObserveMember(ctx context.Context, member events.CommunityMember) error {
	if member.ChatID == 0 || member.UserID == 0 {
		return fmt.Errorf("community member chat and user ids are required")
	}
	observedAt := timestamp(member.LastMessageAt)
	username := strings.TrimPrefix(strings.TrimSpace(member.UserName), "@")
	displayName := strings.TrimSpace(member.DisplayName)
	if displayName == "" {
		displayName = username
	}
	query := s.db.Adopt(`INSERT INTO community_members
		(tenant_id, chat_id, user_id, username, display_name, last_message_id, last_thread_id,
		 last_message_at, first_seen_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (tenant_id, chat_id, user_id) DO UPDATE SET
			username = CASE WHEN excluded.username <> '' THEN excluded.username ELSE community_members.username END,
			display_name = CASE WHEN excluded.display_name <> '' THEN excluded.display_name ELSE community_members.display_name END,
			last_message_id = CASE WHEN excluded.last_message_at >= community_members.last_message_at
				THEN excluded.last_message_id ELSE community_members.last_message_id END,
			last_thread_id = CASE WHEN excluded.last_message_at >= community_members.last_message_at
				THEN excluded.last_thread_id ELSE community_members.last_thread_id END,
			last_message_at = CASE WHEN excluded.last_message_at >= community_members.last_message_at
				THEN excluded.last_message_at ELSE community_members.last_message_at END,
			first_seen_at = CASE WHEN excluded.first_seen_at < community_members.first_seen_at
				THEN excluded.first_seen_at ELSE community_members.first_seen_at END,
			updated_at = excluded.updated_at`)
	s.lock.Lock()
	defer s.lock.Unlock()
	if _, err := s.db.ExecContext(ctx, query, s.db.TenantID(), member.ChatID, member.UserID,
		username, displayName, member.LastMessageID, member.LastThreadID, observedAt, observedAt, time.Now().UTC()); err != nil {
		return fmt.Errorf("observe community member: %w", err)
	}
	return nil
}

// UpdateMemberIdentity refreshes Telegram's current username and display name without
// changing the last-message metadata collected by the moderation pipeline.
func (s *Store) UpdateMemberIdentity(
	ctx context.Context, chatID, userID int64, username, displayName string,
) error {
	if chatID == 0 || userID == 0 {
		return fmt.Errorf("community member chat and user ids are required")
	}
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = username
	}
	query := s.db.Adopt(`UPDATE community_members
		SET username = ?,
			display_name = CASE WHEN ? <> '' THEN ? ELSE display_name END,
			updated_at = ?
		WHERE tenant_id = ? AND chat_id = ? AND user_id = ?`)
	s.lock.Lock()
	defer s.lock.Unlock()
	if _, err := s.db.ExecContext(ctx, query, username, displayName, displayName, time.Now().UTC(),
		s.db.TenantID(), chatID, userID); err != nil {
		return fmt.Errorf("update community member identity: %w", err)
	}
	return nil
}

// GetCommunityMember returns one member by Telegram id.
func (s *Store) GetCommunityMember(
	ctx context.Context, chatID, userID int64,
) (events.CommunityMember, bool, error) {
	var member events.CommunityMember
	query := s.db.Adopt(`SELECT chat_id, user_id, username, display_name,
		last_message_id, last_thread_id, last_message_at
		FROM community_members WHERE tenant_id = ? AND chat_id = ? AND user_id = ?`)
	if err := s.db.GetContext(ctx, &member, query, s.db.TenantID(), chatID, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return events.CommunityMember{}, false, nil
		}
		return events.CommunityMember{}, false, fmt.Errorf("get community member: %w", err)
	}
	return member, true, nil
}

// FindCommunityMemberByUsername resolves a current Telegram username case-insensitively.
func (s *Store) FindCommunityMemberByUsername(
	ctx context.Context, chatID int64, username string,
) (events.CommunityMember, bool, error) {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if username == "" {
		return events.CommunityMember{}, false, nil
	}
	var member events.CommunityMember
	query := s.db.Adopt(`SELECT chat_id, user_id, username, display_name,
		last_message_id, last_thread_id, last_message_at
		FROM community_members
		WHERE tenant_id = ? AND chat_id = ? AND LOWER(username) = LOWER(?)
		ORDER BY updated_at DESC LIMIT 1`)
	if err := s.db.GetContext(ctx, &member, query, s.db.TenantID(), chatID, username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return events.CommunityMember{}, false, nil
		}
		return events.CommunityMember{}, false, fmt.Errorf("find community member: %w", err)
	}
	return member, true, nil
}

// GetCommunityPresentation returns the accepted presentation for one member.
func (s *Store) GetCommunityPresentation(
	ctx context.Context, chatID, userID int64,
) (events.CommunityPresentation, bool, error) {
	var presentation events.CommunityPresentation
	query := s.db.Adopt(`SELECT chat_id, thread_id, user_id, first_message_id, created_at
		FROM community_presentations WHERE tenant_id = ? AND chat_id = ? AND user_id = ?`)
	if err := s.db.GetContext(ctx, &presentation, query, s.db.TenantID(), chatID, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return events.CommunityPresentation{}, false, nil
		}
		return events.CommunityPresentation{}, false, fmt.Errorf("get community presentation: %w", err)
	}
	return presentation, true, nil
}

// CreateCommunityUserReport stores a dashboard-only complaint without triggering moderation.
func (s *Store) CreateCommunityUserReport(ctx context.Context, report events.CommunityUserReport) error {
	if strings.TrimSpace(report.ReportKey) == "" || report.ChatID == 0 ||
		report.ReporterUserID == 0 || report.ReportedUserID == 0 || strings.TrimSpace(report.Reason) == "" {
		return fmt.Errorf("community report key, chat, reporter, reported user, and reason are required")
	}
	query := s.db.Adopt(`INSERT INTO community_user_reports
		(tenant_id, report_key, chat_id, reporter_user_id, reporter_username, reporter_display_name,
		 reported_user_id, reported_username, reported_display_name, source_message_id, source_thread_id,
		 reason, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?)
		ON CONFLICT (tenant_id, report_key) DO NOTHING`)
	if _, err := s.db.ExecContext(ctx, query, s.db.TenantID(), report.ReportKey, report.ChatID,
		report.ReporterUserID, strings.TrimPrefix(report.ReporterUserName, "@"), report.ReporterDisplayName,
		report.ReportedUserID, strings.TrimPrefix(report.ReportedUserName, "@"), report.ReportedDisplayName,
		report.SourceMessageID, report.SourceThreadID, strings.TrimSpace(report.Reason),
		timestamp(report.CreatedAt)); err != nil {
		return fmt.Errorf("create community user report: %w", err)
	}
	return nil
}

// ListMembers searches by id, username, or display name.
func (s *Store) ListMembers(ctx context.Context, filter MemberFilter) ([]MemberRecord, error) {
	limit := boundedLimit(filter.Limit)
	where := []string{"m.tenant_id = ?"}
	args := []any{s.db.TenantID()}
	if filter.ChatID != 0 {
		where = append(where, "m.chat_id = ?")
		args = append(args, filter.ChatID)
	}
	if filter.UserID != 0 {
		where = append(where, "m.user_id = ?")
		args = append(args, filter.UserID)
	}
	if query := strings.TrimPrefix(strings.TrimSpace(filter.Query), "@"); query != "" {
		like := "%" + strings.ToLower(query) + "%"
		where = append(where, `(LOWER(m.username) LIKE ? OR LOWER(m.display_name) LIKE ?
			OR LOWER(CAST(m.user_id AS TEXT)) LIKE ?)`)
		args = append(args, like, like, like)
	}
	args = append(args, limit)
	query := s.db.Adopt(`SELECT m.chat_id, m.user_id, m.username, m.display_name,
		m.last_message_id, m.last_thread_id, m.last_message_at,
		CASE WHEN p.user_id IS NULL THEN ? ELSE ? END AS has_presentation,
		COALESCE(p.first_message_id, 0) AS presentation_message_id,
		COALESCE(p.thread_id, 0) AS presentation_thread_id,
		p.created_at AS presentation_created_at,
		(SELECT COUNT(*) FROM community_user_reports r
			WHERE r.tenant_id = m.tenant_id AND r.chat_id = m.chat_id
			  AND r.reported_user_id = m.user_id) AS report_count,
		(SELECT COUNT(*) FROM community_rule_events e
			WHERE e.tenant_id = m.tenant_id AND e.chat_id = m.chat_id
			  AND e.user_id = m.user_id AND e.action <> 'allow') AS warning_count
		FROM community_members m
		LEFT JOIN community_presentations p
		  ON p.tenant_id = m.tenant_id AND p.chat_id = m.chat_id AND p.user_id = m.user_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY m.last_message_at DESC LIMIT ?`)
	queryArgs := make([]any, 0, 2+len(args))
	queryArgs = append(queryArgs, false, true)
	queryArgs = append(queryArgs, args...)
	var records []MemberRecord
	if err := s.db.SelectContext(ctx, &records, query, queryArgs...); err != nil {
		return nil, fmt.Errorf("list community members: %w", err)
	}
	return records, nil
}

// ListUserReports returns newest complaints first.
func (s *Store) ListUserReports(ctx context.Context, filter UserReportFilter) ([]UserReportRecord, error) {
	where := []string{"tenant_id = ?"}
	args := []any{s.db.TenantID()}
	if filter.ChatID != 0 {
		where = append(where, "chat_id = ?")
		args = append(args, filter.ChatID)
	}
	if filter.ReportedUserID != 0 {
		where = append(where, "reported_user_id = ?")
		args = append(args, filter.ReportedUserID)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	args = append(args, boundedLimit(filter.Limit))
	query := s.db.Adopt(`SELECT report_key, chat_id, reporter_user_id, reporter_username,
		reporter_display_name, reported_user_id, reported_username, reported_display_name,
		source_message_id, source_thread_id, reason, status, created_at
		FROM community_user_reports WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY created_at DESC LIMIT ?`)
	var records []UserReportRecord
	if err := s.db.SelectContext(ctx, &records, query, args...); err != nil {
		return nil, fmt.Errorf("list community user reports: %w", err)
	}
	return records, nil
}

// ResetHistoricalTopicState removes only dry-run topic decisions and one-time claims so a backfill can be rebuilt.
func (s *Store) ResetHistoricalTopicState(
	ctx context.Context, chatID int64, topicIDs []int, since time.Time,
) error {
	if chatID == 0 || len(topicIDs) == 0 || since.IsZero() {
		return fmt.Errorf("chat, topic ids, and start time are required for community history reset")
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin community history reset: %w", err)
	}
	defer tx.Rollback()
	for _, topicID := range topicIDs {
		if _, err = tx.ExecContext(ctx, s.db.Adopt(`DELETE FROM community_rule_events
			WHERE tenant_id = ? AND chat_id = ? AND thread_id = ? AND shadow = ? AND created_at >= ?`),
			s.db.TenantID(), chatID, topicID, true, since.UTC()); err != nil {
			return fmt.Errorf("reset community rule events: %w", err)
		}
		if _, err = tx.ExecContext(ctx, s.db.Adopt(`DELETE FROM community_presentations
			WHERE tenant_id = ? AND chat_id = ? AND thread_id = ? AND created_at >= ?`),
			s.db.TenantID(), chatID, topicID, since.UTC()); err != nil {
			return fmt.Errorf("reset community presentations: %w", err)
		}
		if _, err = tx.ExecContext(ctx, s.db.Adopt(`DELETE FROM community_contest_entries
			WHERE tenant_id = ? AND chat_id = ? AND thread_id = ? AND created_at >= ?`),
			s.db.TenantID(), chatID, topicID, since.UTC()); err != nil {
			return fmt.Errorf("reset community contest entries: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit community history reset: %w", err)
	}
	return nil
}
