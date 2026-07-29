// Package community implements deterministic moderation for structured Telegram topics.
package community

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redstone-md/shield/app/events"
	"github.com/redstone-md/shield/app/moderation"
	"github.com/redstone-md/shield/app/storage/engine"
)

// Config maps the structured topics and rollout policy for one community.
type Config struct {
	Enabled                 bool
	ChatID                  int64
	PresentationThreadID    int
	ContestThreadID         int
	ContestID               string
	Shadow                  bool
	RequirePresentationText bool
}

// Store persists one-time presentations, contest entries, strikes, and audit metadata.
type Store struct {
	db   *engine.SQL
	lock engine.RWLocker
}

// NewStore initializes the community-rule tables.
func NewStore(ctx context.Context, db *engine.SQL) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("db connection is nil")
	}
	store := &Store{db: db, lock: db.MakeLock()}
	schemas := []string{
		`CREATE TABLE IF NOT EXISTS community_presentations (
			tenant_id TEXT NOT NULL,
			chat_id BIGINT NOT NULL,
			thread_id INTEGER NOT NULL,
			user_id BIGINT NOT NULL,
			entry_key TEXT NOT NULL,
			first_message_id INTEGER NOT NULL,
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY (tenant_id, chat_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS community_contest_entries (
			tenant_id TEXT NOT NULL,
			contest_id TEXT NOT NULL,
			chat_id BIGINT NOT NULL,
			thread_id INTEGER NOT NULL,
			user_id BIGINT NOT NULL,
			entry_key TEXT NOT NULL,
			first_message_id INTEGER NOT NULL,
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY (tenant_id, contest_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS community_violations (
			tenant_id TEXT NOT NULL,
			chat_id BIGINT NOT NULL,
			user_id BIGINT NOT NULL,
			rule_code TEXT NOT NULL,
			strikes INTEGER NOT NULL DEFAULT 0,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (tenant_id, chat_id, user_id, rule_code)
		)`,
		`CREATE TABLE IF NOT EXISTS community_rule_events (
			tenant_id TEXT NOT NULL,
			event_key TEXT NOT NULL,
			chat_id BIGINT NOT NULL,
			thread_id INTEGER NOT NULL,
			message_id INTEGER NOT NULL,
			user_id BIGINT NOT NULL,
			rule_code TEXT NOT NULL,
			action TEXT NOT NULL,
			reason TEXT NOT NULL,
			user_message TEXT NOT NULL DEFAULT '',
			duration_seconds BIGINT NOT NULL DEFAULT 0,
			shadow BOOLEAN NOT NULL,
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY (tenant_id, event_key)
		)`,
		`CREATE TABLE IF NOT EXISTS community_members (
			tenant_id TEXT NOT NULL,
			chat_id BIGINT NOT NULL,
			user_id BIGINT NOT NULL,
			username TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			last_message_id INTEGER NOT NULL DEFAULT 0,
			last_thread_id INTEGER NOT NULL DEFAULT 0,
			last_message_at TIMESTAMP NOT NULL,
			first_seen_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (tenant_id, chat_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS community_user_reports (
			tenant_id TEXT NOT NULL,
			report_key TEXT NOT NULL,
			chat_id BIGINT NOT NULL,
			reporter_user_id BIGINT NOT NULL,
			reporter_username TEXT NOT NULL DEFAULT '',
			reporter_display_name TEXT NOT NULL DEFAULT '',
			reported_user_id BIGINT NOT NULL,
			reported_username TEXT NOT NULL DEFAULT '',
			reported_display_name TEXT NOT NULL DEFAULT '',
			source_message_id INTEGER NOT NULL DEFAULT 0,
			source_thread_id INTEGER NOT NULL DEFAULT 0,
			reason TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'open',
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY (tenant_id, report_key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_community_events_topic
			ON community_rule_events(tenant_id, chat_id, thread_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_community_members_identity
			ON community_members(tenant_id, chat_id, username, display_name)`,
		`CREATE INDEX IF NOT EXISTS idx_community_user_reports_target
			ON community_user_reports(tenant_id, chat_id, reported_user_id, created_at)`,
	}
	store.lock.Lock()
	defer store.lock.Unlock()
	for _, schema := range schemas {
		if _, err := db.ExecContext(ctx, schema); err != nil {
			return nil, fmt.Errorf("initialize community storage: %w", err)
		}
	}
	migrations := []string{
		"ALTER TABLE community_rule_events ADD COLUMN user_message TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE community_rule_events ADD COLUMN duration_seconds BIGINT NOT NULL DEFAULT 0",
	}
	if db.Type() == engine.Postgres {
		migrations = []string{
			"ALTER TABLE community_rule_events ADD COLUMN IF NOT EXISTS user_message TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE community_rule_events ADD COLUMN IF NOT EXISTS duration_seconds BIGINT NOT NULL DEFAULT 0",
		}
	}
	for _, migration := range migrations {
		if _, err := db.ExecContext(ctx, migration); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return nil, fmt.Errorf("migrate community storage: %w", err)
		}
	}
	return store, nil
}

// Engine evaluates deterministic rules without sending message content to an LLM.
type Engine struct {
	store  *Store
	config Config
	mu     sync.Mutex
}

// NewEngine creates a deterministic community rules engine.
func NewEngine(store *Store, config Config) (*Engine, error) {
	if store == nil {
		return nil, fmt.Errorf("community store is nil")
	}
	if config.Enabled {
		if config.ChatID == 0 {
			return nil, fmt.Errorf("community chat id is required")
		}
		if config.PresentationThreadID <= 0 || config.ContestThreadID <= 0 {
			return nil, fmt.Errorf("presentation and contest thread ids are required")
		}
		if config.PresentationThreadID == config.ContestThreadID {
			return nil, fmt.Errorf("presentation and contest threads must be different")
		}
		if strings.TrimSpace(config.ContestID) == "" {
			return nil, fmt.Errorf("contest id is required")
		}
	}
	return &Engine{store: store, config: config}, nil
}

// Evaluate applies topic-specific rules and records the decision without raw message text.
func (e *Engine) Evaluate(ctx context.Context, msg events.CommunityMessage) (events.CommunityDecision, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.config.Enabled || msg.ChatID != e.config.ChatID {
		return events.CommunityDecision{}, nil
	}
	if msg.UserID != 0 && !msg.IsBot {
		if err := e.store.observeMember(ctx, msg); err != nil {
			return events.CommunityDecision{}, err
		}
	}
	if msg.ThreadID != e.config.PresentationThreadID && msg.ThreadID != e.config.ContestThreadID {
		return events.CommunityDecision{}, nil
	}
	if existing, found, err := e.store.decisionForMessage(ctx, msg); err != nil {
		return events.CommunityDecision{}, err
	} else if found {
		return existing, nil
	}
	if msg.IsAdministrator || msg.IsBot || msg.UserID == 0 {
		return e.record(ctx, msg, events.CommunityDecision{
			Handled: true, Enforce: false, Action: moderation.ActionAllow,
			Rule: "administrator_bypass", Reason: "administrator, channel, or bot message",
		})
	}
	if msg.ThreadID == e.config.PresentationThreadID {
		return e.evaluatePresentation(ctx, msg)
	}
	return e.evaluateContest(ctx, msg)
}

func (e *Engine) evaluatePresentation(
	ctx context.Context, msg events.CommunityMessage,
) (events.CommunityDecision, error) {
	if msg.IsReply || !msg.HasPhoto || msg.HasVideo {
		return e.violation(ctx, msg, "presentation_message_not_allowed",
			"Se ha enviado un mensaje en Presentaciones; aquí solo se permiten presentaciones reales con foto y texto.",
			"En Presentaciones solo se permite publicar una presentación real con foto y texto. "+
				"No se permiten respuestas, conversación, texto suelto ni vídeos.")
	}
	if e.config.RequirePresentationText && msg.MediaGroupID == "" && strings.TrimSpace(msg.Text) == "" {
		return e.violation(ctx, msg, "presentation_incomplete", "Se ha publicado una foto sin texto de presentación.",
			"La foto necesita un texto de presentación para considerarse una presentación válida.")
	}

	entryKey := domainEntryKey(msg)
	claimed, existingKey, err := e.store.claimPresentation(ctx, msg, entryKey)
	if err != nil {
		return events.CommunityDecision{}, err
	}
	if claimed || existingKey == entryKey {
		return e.record(ctx, msg, events.CommunityDecision{
			Handled: true, Enforce: false, Action: moderation.ActionAllow,
			Rule: "presentation_once", Reason: "Primera presentación válida o continuación del mismo álbum.",
		})
	}
	return e.violation(ctx, msg, "presentation_duplicate", "La persona ya tiene una presentación registrada.",
		"Solo se permite una presentación por persona. Esta publicación se ha retirado.")
}

func (e *Engine) evaluateContest(
	ctx context.Context, msg events.CommunityMessage,
) (events.CommunityDecision, error) {
	if msg.IsReply || !msg.HasPhoto || msg.HasVideo {
		return e.violation(ctx, msg, "contest_format",
			"contest accepts one photo entry or one photo album; replies, text-only posts, and videos are not accepted",
			"En el Concurso solo se admite una entrada de fotos por persona. No publiques respuestas, texto suelto ni vídeos.")
	}

	entryKey := domainEntryKey(msg)
	claimed, existingKey, err := e.store.claimContest(ctx, msg, e.config.ContestID, entryKey)
	if err != nil {
		return events.CommunityDecision{}, err
	}
	if claimed || existingKey == entryKey {
		return e.record(ctx, msg, events.CommunityDecision{
			Handled: true, Enforce: false, Action: moderation.ActionAllow,
			Rule: "contest_entry_once", Reason: "first contest entry or continuation of the same album",
		})
	}
	return e.violation(ctx, msg, "contest_duplicate", "user already entered the current contest",
		"Solo se permite una entrada por persona en este concurso. Esta publicación se ha retirado.")
}

func (e *Engine) violation(
	ctx context.Context, msg events.CommunityMessage, rule, reason, userMessage string,
) (events.CommunityDecision, error) {
	return e.store.recordViolationDecision(ctx, msg, rule, reason, userMessage, e.config.Shadow)
}

func violationDecision(strikes int, rule, reason, userMessage string, shadow bool) events.CommunityDecision {
	decision := events.CommunityDecision{
		Handled: true, Enforce: !shadow, Action: moderation.ActionWarn,
		Rule: rule, Reason: reason, UserMessage: userMessage,
	}
	switch strikes {
	case 1:
		decision.Action = moderation.ActionWarn
	case 2:
		decision.Action = moderation.ActionRestrict
		decision.Duration = time.Hour
		decision.UserMessage += " Además, la cuenta queda restringida durante 1 hora."
	case 3:
		decision.Action = moderation.ActionRestrict
		decision.Duration = 24 * time.Hour
		decision.UserMessage += " Además, la cuenta queda restringida durante 24 horas."
	default:
		decision.Action = moderation.ActionBan
		decision.UserMessage += " La reincidencia reiterada conlleva la expulsión."
	}
	return decision
}

func (e *Engine) record(
	ctx context.Context, msg events.CommunityMessage, decision events.CommunityDecision,
) (events.CommunityDecision, error) {
	if err := e.store.recordDecision(ctx, msg, decision, e.config.Shadow); err != nil {
		return events.CommunityDecision{}, err
	}
	return decision, nil
}

func domainEntryKey(msg events.CommunityMessage) string {
	if msg.MediaGroupID != "" {
		return "album:" + msg.MediaGroupID
	}
	return fmt.Sprintf("message:%d", msg.MessageID)
}

func (s *Store) claimPresentation(
	ctx context.Context, msg events.CommunityMessage, entryKey string,
) (claimed bool, existingKey string, err error) {
	return s.claim(ctx, "community_presentations", msg, "", entryKey)
}

func (s *Store) claimContest(
	ctx context.Context, msg events.CommunityMessage, contestID, entryKey string,
) (claimed bool, existingKey string, err error) {
	return s.claim(ctx, "community_contest_entries", msg, contestID, entryKey)
}

func (s *Store) claim(
	ctx context.Context, table string, msg events.CommunityMessage, contestID, entryKey string,
) (claimed bool, existingKey string, err error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return false, "", fmt.Errorf("begin community claim: %w", err)
	}
	defer tx.Rollback()

	var result sql.Result
	if table == "community_presentations" {
		query := s.db.Adopt(`INSERT INTO community_presentations
			(tenant_id, chat_id, thread_id, user_id, entry_key, first_message_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (tenant_id, chat_id, user_id) DO NOTHING`)
		result, err = tx.ExecContext(ctx, query, msg.TenantID, msg.ChatID, msg.ThreadID, msg.UserID,
			entryKey, msg.MessageID, timestamp(msg.ReceivedAt))
	} else {
		query := s.db.Adopt(`INSERT INTO community_contest_entries
			(tenant_id, contest_id, chat_id, thread_id, user_id, entry_key, first_message_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (tenant_id, contest_id, user_id) DO NOTHING`)
		result, err = tx.ExecContext(ctx, query, msg.TenantID, contestID, msg.ChatID, msg.ThreadID, msg.UserID,
			entryKey, msg.MessageID, timestamp(msg.ReceivedAt))
	}
	if err != nil {
		return false, "", fmt.Errorf("claim community entry: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, "", fmt.Errorf("inspect community claim: %w", err)
	}
	if rows > 0 {
		if err = tx.Commit(); err != nil {
			return false, "", fmt.Errorf("commit community claim: %w", err)
		}
		return true, entryKey, nil
	}

	var existing string
	if table == "community_presentations" {
		query := s.db.Adopt(`SELECT entry_key FROM community_presentations
			WHERE tenant_id = ? AND chat_id = ? AND user_id = ?`)
		err = tx.GetContext(ctx, &existing, query, msg.TenantID, msg.ChatID, msg.UserID)
	} else {
		query := s.db.Adopt(`SELECT entry_key FROM community_contest_entries
			WHERE tenant_id = ? AND contest_id = ? AND user_id = ?`)
		err = tx.GetContext(ctx, &existing, query, msg.TenantID, contestID, msg.UserID)
	}
	if err != nil {
		return false, "", fmt.Errorf("load existing community entry: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return false, "", fmt.Errorf("commit community lookup: %w", err)
	}
	return false, existing, nil
}

func (s *Store) recordViolationDecision(
	ctx context.Context,
	msg events.CommunityMessage,
	rule string,
	reason string,
	userMessage string,
	shadow bool,
) (events.CommunityDecision, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return events.CommunityDecision{}, fmt.Errorf("begin community violation: %w", err)
	}
	defer tx.Rollback()

	var strikes int
	if shadow {
		query := s.db.Adopt(`SELECT COUNT(*) FROM community_rule_events
			WHERE tenant_id = ? AND chat_id = ? AND user_id = ? AND rule_code = ? AND shadow = ?`)
		if err = tx.GetContext(ctx, &strikes, query, msg.TenantID, msg.ChatID, msg.UserID, rule, true); err != nil {
			return events.CommunityDecision{}, fmt.Errorf("count shadow community violations: %w", err)
		}
		strikes++
	} else {
		query := s.db.Adopt(`INSERT INTO community_violations
			(tenant_id, chat_id, user_id, rule_code, strikes, updated_at)
			VALUES (?, ?, ?, ?, 1, ?)
			ON CONFLICT (tenant_id, chat_id, user_id, rule_code)
			DO UPDATE SET strikes = community_violations.strikes + 1, updated_at = excluded.updated_at`)
		if _, err = tx.ExecContext(ctx, query, msg.TenantID, msg.ChatID, msg.UserID, rule,
			timestamp(msg.ReceivedAt)); err != nil {
			return events.CommunityDecision{}, fmt.Errorf("increment community violation: %w", err)
		}
		query = s.db.Adopt(`SELECT strikes FROM community_violations
			WHERE tenant_id = ? AND chat_id = ? AND user_id = ? AND rule_code = ?`)
		if err = tx.GetContext(ctx, &strikes, query, msg.TenantID, msg.ChatID, msg.UserID, rule); err != nil {
			return events.CommunityDecision{}, fmt.Errorf("load community violation count: %w", err)
		}
	}

	decision := violationDecision(strikes, rule, reason, userMessage, shadow)
	eventKey := fmt.Sprintf("%s:%d:%d", decision.Rule, msg.ChatID, msg.MessageID)
	query := s.db.Adopt(`INSERT INTO community_rule_events
		(tenant_id, event_key, chat_id, thread_id, message_id, user_id, rule_code, action, reason,
		 user_message, duration_seconds, shadow, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (tenant_id, event_key) DO NOTHING`)
	if _, err = tx.ExecContext(ctx, query, msg.TenantID, eventKey, msg.ChatID, msg.ThreadID, msg.MessageID,
		msg.UserID, decision.Rule, string(decision.Action), decision.Reason, decision.UserMessage,
		int64(decision.Duration/time.Second), shadow, timestamp(msg.ReceivedAt)); err != nil {
		return events.CommunityDecision{}, fmt.Errorf("record community violation: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return events.CommunityDecision{}, fmt.Errorf("commit community violation: %w", err)
	}
	return decision, nil
}

func (s *Store) recordDecision(
	ctx context.Context, msg events.CommunityMessage, decision events.CommunityDecision, shadow bool,
) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	eventKey := fmt.Sprintf("%s:%d:%d", decision.Rule, msg.ChatID, msg.MessageID)
	query := s.db.Adopt(`INSERT INTO community_rule_events
		(tenant_id, event_key, chat_id, thread_id, message_id, user_id, rule_code, action, reason,
		 user_message, duration_seconds, shadow, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (tenant_id, event_key) DO NOTHING`)
	if _, err := s.db.ExecContext(ctx, query, msg.TenantID, eventKey, msg.ChatID, msg.ThreadID, msg.MessageID,
		msg.UserID, decision.Rule, string(decision.Action), decision.Reason, decision.UserMessage,
		int64(decision.Duration/time.Second), shadow, timestamp(msg.ReceivedAt)); err != nil {
		return fmt.Errorf("record community decision: %w", err)
	}
	return nil
}

func (s *Store) decisionForMessage(
	ctx context.Context, msg events.CommunityMessage,
) (events.CommunityDecision, bool, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	var stored struct {
		Rule            string `db:"rule_code"`
		Action          string `db:"action"`
		Reason          string `db:"reason"`
		UserMessage     string `db:"user_message"`
		DurationSeconds int64  `db:"duration_seconds"`
		Shadow          bool   `db:"shadow"`
	}
	query := s.db.Adopt(`SELECT rule_code, action, reason, user_message, duration_seconds, shadow
		FROM community_rule_events
		WHERE tenant_id = ? AND chat_id = ? AND message_id = ? AND user_id = ?
		ORDER BY created_at ASC LIMIT 1`)
	if err := s.db.GetContext(ctx, &stored, query, msg.TenantID, msg.ChatID, msg.MessageID, msg.UserID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return events.CommunityDecision{}, false, nil
		}
		return events.CommunityDecision{}, false, fmt.Errorf("load prior community decision: %w", err)
	}
	action := moderation.Action(stored.Action)
	return events.CommunityDecision{
		Handled:     true,
		Enforce:     !stored.Shadow && action != moderation.ActionAllow,
		Action:      action,
		Rule:        stored.Rule,
		Reason:      stored.Reason,
		UserMessage: stored.UserMessage,
		Duration:    time.Duration(stored.DurationSeconds) * time.Second,
	}, true, nil
}

func timestamp(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}
