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
		`CREATE TABLE IF NOT EXISTS community_contests (
			tenant_id TEXT NOT NULL,
			contest_id TEXT NOT NULL,
			chat_id BIGINT NOT NULL,
			thread_id INTEGER NOT NULL DEFAULT 0,
			title TEXT NOT NULL,
			bases TEXT NOT NULL,
			announcement_text TEXT NOT NULL,
			status TEXT NOT NULL,
			announcement_message_id INTEGER NOT NULL DEFAULT 0,
			deadline_at TIMESTAMP NULL,
			published_at TIMESTAMP NULL,
			reaction_cutoff_at TIMESTAMP NULL,
			consolidate_after TIMESTAMP NULL,
			finalized_at TIMESTAMP NULL,
			winner_user_id BIGINT NOT NULL DEFAULT 0,
			winner_message_id INTEGER NOT NULL DEFAULT 0,
			winner_reactions INTEGER NOT NULL DEFAULT 0,
			final_snapshot TEXT NOT NULL DEFAULT '',
			created_by TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (tenant_id, contest_id)
		)`,
		`CREATE TABLE IF NOT EXISTS community_contest_reactions (
			tenant_id TEXT NOT NULL,
			contest_id TEXT NOT NULL,
			message_id INTEGER NOT NULL,
			reaction_key TEXT NOT NULL,
			total_count INTEGER NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (tenant_id, contest_id, message_id, reaction_key)
		)`,
		`CREATE TABLE IF NOT EXISTS community_contest_reaction_actors (
			tenant_id TEXT NOT NULL,
			contest_id TEXT NOT NULL,
			message_id INTEGER NOT NULL,
			actor_key TEXT NOT NULL,
			reaction_key TEXT NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (tenant_id, contest_id, message_id, actor_key, reaction_key)
		)`,
		`CREATE TABLE IF NOT EXISTS community_contest_appeals (
			tenant_id TEXT NOT NULL,
			appeal_token TEXT NOT NULL,
			contest_id TEXT NOT NULL,
			chat_id BIGINT NOT NULL,
			user_id BIGINT NOT NULL,
			username TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			original_message_id INTEGER NOT NULL,
			duplicate_message_id INTEGER NOT NULL,
			photo_file_id TEXT NOT NULL DEFAULT '',
			caption TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'offered',
			appeal_text TEXT NOT NULL DEFAULT '',
			resolution_text TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (tenant_id, appeal_token)
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
			related_message_id INTEGER NOT NULL DEFAULT 0,
			media_group_id TEXT NOT NULL DEFAULT '',
			user_id BIGINT NOT NULL,
			rule_code TEXT NOT NULL,
			action TEXT NOT NULL,
			reason TEXT NOT NULL,
			message_text TEXT NOT NULL DEFAULT '',
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
		`CREATE INDEX IF NOT EXISTS idx_community_contests_topic
			ON community_contests(tenant_id, chat_id, thread_id, status)`,
		`CREATE INDEX IF NOT EXISTS idx_community_contest_appeals_status
			ON community_contest_appeals(tenant_id, status, created_at)`,
	}
	store.lock.Lock()
	defer store.lock.Unlock()
	for _, schema := range schemas {
		if _, err := db.ExecContext(ctx, schema); err != nil {
			return nil, fmt.Errorf("initialize community storage: %w", err)
		}
	}
	migrations := []string{
		"ALTER TABLE community_rule_events ADD COLUMN related_message_id INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE community_rule_events ADD COLUMN media_group_id TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE community_rule_events ADD COLUMN message_text TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE community_rule_events ADD COLUMN user_message TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE community_rule_events ADD COLUMN duration_seconds BIGINT NOT NULL DEFAULT 0",
		"ALTER TABLE community_rule_events ADD COLUMN contest_action BOOLEAN NOT NULL DEFAULT 0",
		"ALTER TABLE community_rule_events ADD COLUMN appeal_token TEXT NOT NULL DEFAULT ''",
	}
	if db.Type() == engine.Postgres {
		migrations = []string{
			"ALTER TABLE community_rule_events ADD COLUMN IF NOT EXISTS related_message_id INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE community_rule_events ADD COLUMN IF NOT EXISTS media_group_id TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE community_rule_events ADD COLUMN IF NOT EXISTS message_text TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE community_rule_events ADD COLUMN IF NOT EXISTS user_message TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE community_rule_events ADD COLUMN IF NOT EXISTS duration_seconds BIGINT NOT NULL DEFAULT 0",
			"ALTER TABLE community_rule_events ADD COLUMN IF NOT EXISTS contest_action BOOLEAN NOT NULL DEFAULT false",
			"ALTER TABLE community_rule_events ADD COLUMN IF NOT EXISTS appeal_token TEXT NOT NULL DEFAULT ''",
		}
	}
	for _, migration := range migrations {
		if _, err := db.ExecContext(ctx, migration); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return nil, fmt.Errorf("migrate community storage: %w", err)
		}
	}
	backfill := db.Adopt(`UPDATE community_rule_events
		SET media_group_id = COALESCE((
			SELECT incoming.media_group_id
			FROM incoming_events incoming
			WHERE incoming.tenant_id = community_rule_events.tenant_id
			  AND incoming.chat_id = community_rule_events.chat_id
			  AND incoming.message_id = community_rule_events.message_id
			LIMIT 1
		), '')
		WHERE media_group_id = ''`)
	if _, err := db.ExecContext(ctx, backfill); err != nil &&
		!strings.Contains(err.Error(), "no such table") &&
		!strings.Contains(err.Error(), "does not exist") {
		return nil, fmt.Errorf("backfill community media groups: %w", err)
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
		if config.PresentationThreadID <= 0 {
			return nil, fmt.Errorf("presentation thread id is required")
		}
		if config.ContestThreadID > 0 && config.PresentationThreadID == config.ContestThreadID {
			return nil, fmt.Errorf("presentation and contest threads must be different")
		}
	}
	return &Engine{store: store, config: config}, nil
}

// Evaluate applies topic-specific rules and records the decision with bounded-retention evidence.
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
	activeContest, dynamicContest, err := e.store.ActiveContestForThread(ctx, msg.ChatID, msg.ThreadID)
	if err != nil {
		return events.CommunityDecision{}, err
	}
	legacyContest := msg.ThreadID == e.config.ContestThreadID && strings.TrimSpace(e.config.ContestID) != ""
	if msg.ThreadID != e.config.PresentationThreadID && !dynamicContest && !legacyContest {
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
	if dynamicContest {
		return e.evaluateContest(ctx, msg, activeContest.ContestID, true)
	}
	return e.evaluateContest(ctx, msg, e.config.ContestID, false)
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
	claimed, existingKey, existingMessageID, err := e.store.claimPresentation(ctx, msg, entryKey)
	if err != nil {
		return events.CommunityDecision{}, err
	}
	if claimed || existingKey == entryKey {
		if !claimed {
			return events.CommunityDecision{
				Handled: true, Enforce: false, GroupedContinuation: true,
				Action: moderation.ActionAllow, Rule: "presentation_once",
				Reason: "Continuación del mismo álbum de presentación.",
			}, nil
		}
		return e.record(ctx, msg, events.CommunityDecision{
			Handled: true, Enforce: false, Action: moderation.ActionAllow,
			Rule: "presentation_once", Reason: "Primera presentación válida o continuación del mismo álbum.",
		})
	}
	return e.violationRelated(ctx, msg, "presentation_duplicate",
		"La persona ya tiene una presentación registrada.",
		"Solo se permite una presentación por persona. Esta publicación se ha retirado.",
		existingMessageID)
}

func (e *Engine) evaluateContest(
	ctx context.Context, msg events.CommunityMessage, contestID string, strictDynamic bool,
) (events.CommunityDecision, error) {
	invalid := msg.IsReply || !msg.HasPhoto || msg.HasVideo || (strictDynamic && msg.MediaGroupID != "")
	if invalid && !strictDynamic {
		return e.violation(ctx, msg, "contest_format",
			"contest accepts one photo entry or one photo album; replies, text-only posts, and videos are not accepted",
			"En el Concurso solo se admite una entrada de fotos por persona. No publiques respuestas, texto suelto ni vídeos.")
	}
	if invalid {
		return e.contestViolation(ctx, msg, "contest_format",
			"El concurso admite exactamente una foto por participación; no admite álbumes, respuestas, texto suelto ni vídeos.",
			"En este concurso solo puedes publicar una foto. Se ha retirado el mensaje; "+
				"los álbumes, respuestas, texto suelto y vídeos no cuentan como participación.",
			contestID, 0, false, true)
	}

	entryKey := domainEntryKey(msg)
	claimed, existingKey, existingMessageID, err := e.store.claimContest(ctx, msg, contestID, entryKey)
	if err != nil {
		return events.CommunityDecision{}, err
	}
	if claimed || existingKey == entryKey {
		if !claimed {
			return events.CommunityDecision{
				Handled: true, Enforce: false, GroupedContinuation: true,
				Action: moderation.ActionAllow, Rule: "contest_entry_once",
				Reason: "continuation of the same contest album",
			}, nil
		}
		return e.record(ctx, msg, events.CommunityDecision{
			Handled: true, Enforce: false, Action: moderation.ActionAllow,
			Rule: "contest_entry_once", Reason: "first contest entry or continuation of the same album",
		})
	}
	if !strictDynamic {
		return e.violationRelated(ctx, msg, "contest_duplicate", "user already entered the current contest",
			"Solo se permite una entrada por persona en este concurso. Esta publicación se ha retirado.", existingMessageID)
	}
	return e.contestViolation(ctx, msg, "contest_duplicate", "La persona ya había participado en este concurso.",
		"Ya has participado en este concurso. Hemos retirado la foto duplicada; tu primera foto sigue participando.",
		contestID, existingMessageID, true, true)
}

func (e *Engine) contestViolation(ctx context.Context, msg events.CommunityMessage, rule, reason,
	userMessage, contestID string, relatedMessageID int, appealable, applyActions bool,
) (events.CommunityDecision, error) {
	decision := events.CommunityDecision{Handled: true, Enforce: applyActions, Action: moderation.ActionWarn,
		Rule: rule, Reason: reason, UserMessage: userMessage, RelatedMessageID: relatedMessageID,
		ContestAction: applyActions}
	if appealable && applyActions {
		token, err := e.store.CreateContestAppealOffer(ctx, contestID, eventsCommunityMessageAlias{
			ChatID: msg.ChatID, UserID: msg.UserID, MessageID: msg.MessageID, UserName: msg.UserName,
			DisplayName: msg.DisplayName, PhotoFileID: msg.PhotoFileID, Text: msg.Text, ReceivedAt: msg.ReceivedAt,
		}, relatedMessageID)
		if err != nil {
			return events.CommunityDecision{}, fmt.Errorf("create contest appeal offer: %w", err)
		}
		decision.AppealToken = token
	}
	if err := e.store.recordDecision(ctx, msg, decision, !applyActions); err != nil {
		return events.CommunityDecision{}, err
	}
	return decision, nil
}

func (e *Engine) violation(
	ctx context.Context, msg events.CommunityMessage, rule, reason, userMessage string,
) (events.CommunityDecision, error) {
	return e.violationRelated(ctx, msg, rule, reason, userMessage, 0)
}

func (e *Engine) violationRelated(
	ctx context.Context,
	msg events.CommunityMessage,
	rule string,
	reason string,
	userMessage string,
	relatedMessageID int,
) (events.CommunityDecision, error) {
	return e.store.recordViolationDecision(
		ctx, msg, rule, reason, userMessage, relatedMessageID, e.config.Shadow,
	)
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
) (claimed bool, existingKey string, existingMessageID int, err error) {
	return s.claim(ctx, "community_presentations", msg, "", entryKey)
}

func (s *Store) claimContest(
	ctx context.Context, msg events.CommunityMessage, contestID, entryKey string,
) (claimed bool, existingKey string, existingMessageID int, err error) {
	return s.claim(ctx, "community_contest_entries", msg, contestID, entryKey)
}

func (s *Store) claim(
	ctx context.Context, table string, msg events.CommunityMessage, contestID, entryKey string,
) (claimed bool, existingKey string, existingMessageID int, err error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return false, "", 0, fmt.Errorf("begin community claim: %w", err)
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
		return false, "", 0, fmt.Errorf("claim community entry: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, "", 0, fmt.Errorf("inspect community claim: %w", err)
	}
	if rows > 0 {
		if err = tx.Commit(); err != nil {
			return false, "", 0, fmt.Errorf("commit community claim: %w", err)
		}
		return true, entryKey, msg.MessageID, nil
	}

	var existing struct {
		EntryKey       string `db:"entry_key"`
		FirstMessageID int    `db:"first_message_id"`
	}
	if table == "community_presentations" {
		query := s.db.Adopt(`SELECT entry_key, first_message_id FROM community_presentations
			WHERE tenant_id = ? AND chat_id = ? AND user_id = ?`)
		err = tx.GetContext(ctx, &existing, query, msg.TenantID, msg.ChatID, msg.UserID)
	} else {
		query := s.db.Adopt(`SELECT entry_key, first_message_id FROM community_contest_entries
			WHERE tenant_id = ? AND contest_id = ? AND user_id = ?`)
		err = tx.GetContext(ctx, &existing, query, msg.TenantID, contestID, msg.UserID)
	}
	if err != nil {
		return false, "", 0, fmt.Errorf("load existing community entry: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return false, "", 0, fmt.Errorf("commit community lookup: %w", err)
	}
	return false, existing.EntryKey, existing.FirstMessageID, nil
}

func (s *Store) recordViolationDecision(
	ctx context.Context,
	msg events.CommunityMessage,
	rule string,
	reason string,
	userMessage string,
	relatedMessageID int,
	shadow bool,
) (events.CommunityDecision, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return events.CommunityDecision{}, fmt.Errorf("begin community violation: %w", err)
	}
	defer tx.Rollback()

	eventKey := communityEventKey(rule, msg)
	var stored struct {
		Rule             string `db:"rule_code"`
		Action           string `db:"action"`
		Reason           string `db:"reason"`
		UserMessage      string `db:"user_message"`
		RelatedMessageID int    `db:"related_message_id"`
		DurationSeconds  int64  `db:"duration_seconds"`
		Shadow           bool   `db:"shadow"`
		ContestAction    bool   `db:"contest_action"`
		AppealToken      string `db:"appeal_token"`
	}
	existingQuery := s.db.Adopt(`SELECT rule_code, action, reason, user_message, related_message_id,
		duration_seconds, shadow, contest_action, appeal_token
		FROM community_rule_events WHERE tenant_id = ? AND event_key = ?`)
	if existingErr := tx.GetContext(ctx, &stored, existingQuery, msg.TenantID, eventKey); existingErr == nil {
		action := moderation.Action(stored.Action)
		return events.CommunityDecision{
			Handled: true, Enforce: !stored.Shadow && action != moderation.ActionAllow,
			GroupedContinuation: true, Action: action, Rule: stored.Rule, Reason: stored.Reason,
			UserMessage: stored.UserMessage, RelatedMessageID: stored.RelatedMessageID,
			Duration:      time.Duration(stored.DurationSeconds) * time.Second,
			ContestAction: stored.ContestAction, AppealToken: stored.AppealToken,
		}, nil
	} else if !errors.Is(existingErr, sql.ErrNoRows) {
		return events.CommunityDecision{}, fmt.Errorf("load grouped community violation: %w", existingErr)
	}

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
	decision.RelatedMessageID = relatedMessageID
	query := s.db.Adopt(`INSERT INTO community_rule_events
		(tenant_id, event_key, chat_id, thread_id, message_id, related_message_id, media_group_id, user_id, rule_code,
		 action, reason, message_text, user_message, duration_seconds, shadow, contest_action, appeal_token, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (tenant_id, event_key) DO NOTHING`)
	if _, err = tx.ExecContext(
		ctx, query, msg.TenantID, eventKey, msg.ChatID, msg.ThreadID, msg.MessageID,
		decision.RelatedMessageID, msg.MediaGroupID, msg.UserID, decision.Rule,
		string(decision.Action), decision.Reason, msg.Text,
		decision.UserMessage,
		int64(decision.Duration/time.Second), shadow, decision.ContestAction,
		decision.AppealToken, timestamp(msg.ReceivedAt),
	); err != nil {
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

	eventKey := communityEventKey(decision.Rule, msg)
	query := s.db.Adopt(`INSERT INTO community_rule_events
		(tenant_id, event_key, chat_id, thread_id, message_id, related_message_id, media_group_id, user_id, rule_code,
		 action, reason, message_text, user_message, duration_seconds, shadow, contest_action, appeal_token, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (tenant_id, event_key) DO NOTHING`)
	if _, err := s.db.ExecContext(
		ctx, query, msg.TenantID, eventKey, msg.ChatID, msg.ThreadID, msg.MessageID,
		decision.RelatedMessageID, msg.MediaGroupID, msg.UserID, decision.Rule,
		string(decision.Action), decision.Reason, msg.Text,
		decision.UserMessage,
		int64(decision.Duration/time.Second), shadow, decision.ContestAction,
		decision.AppealToken, timestamp(msg.ReceivedAt),
	); err != nil {
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
		Rule             string `db:"rule_code"`
		Action           string `db:"action"`
		Reason           string `db:"reason"`
		UserMessage      string `db:"user_message"`
		RelatedMessageID int    `db:"related_message_id"`
		DurationSeconds  int64  `db:"duration_seconds"`
		Shadow           bool   `db:"shadow"`
		ContestAction    bool   `db:"contest_action"`
		AppealToken      string `db:"appeal_token"`
	}
	query := s.db.Adopt(`SELECT rule_code, action, reason, user_message, related_message_id,
		duration_seconds, shadow, contest_action, appeal_token
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
		Handled:          true,
		Enforce:          !stored.Shadow && action != moderation.ActionAllow,
		Action:           action,
		Rule:             stored.Rule,
		Reason:           stored.Reason,
		UserMessage:      stored.UserMessage,
		RelatedMessageID: stored.RelatedMessageID,
		Duration:         time.Duration(stored.DurationSeconds) * time.Second,
		ContestAction:    stored.ContestAction,
		AppealToken:      stored.AppealToken,
	}, true, nil
}

func communityEventKey(rule string, msg events.CommunityMessage) string {
	if msg.MediaGroupID != "" {
		return fmt.Sprintf("%s:%d:album:%s", rule, msg.ChatID, msg.MediaGroupID)
	}
	return fmt.Sprintf("%s:%d:message:%d", rule, msg.ChatID, msg.MessageID)
}

func timestamp(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}
