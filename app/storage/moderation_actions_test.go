package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/redstone-md/shield/app/storage/engine"
)

func TestModerationActionsAdd(t *testing.T) {
	db, err := engine.NewSqlite(":memory:", "gr1")
	require.NoError(t, err)
	defer db.Close()

	store, err := NewModerationActions(context.Background(), db)
	require.NoError(t, err)

	err = store.Add(context.Background(), ModerationActionEntry{
		EventID:        "evt-1",
		CorrelationID:  "corr-1",
		IdempotencyKey: "key-1",
		Command:        "ban_user",
		Status:         "completed",
		ChatID:         123,
		SubjectID:      42,
		Attempt:        1,
		CreatedAt:      time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	err = store.Add(context.Background(), ModerationActionEntry{
		EventID:        "evt-1",
		CorrelationID:  "corr-1",
		IdempotencyKey: "key-1",
		Command:        "delete_message",
		Status:         "failed",
		ChatID:         123,
		SubjectID:      42,
		MessageID:      77,
		Attempt:        2,
		LastError:      "message not found",
		CreatedAt:      time.Date(2026, 4, 22, 12, 1, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	entries, err := store.ByEventID(context.Background(), "evt-1")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "gr1", entries[0].GID)
	assert.Equal(t, "ban_user", entries[0].Command)
	assert.Equal(t, "completed", entries[0].Status)
	assert.Equal(t, "delete_message", entries[1].Command)
	assert.Equal(t, "failed", entries[1].Status)
	assert.Equal(t, "message not found", entries[1].LastError)

	recent, err := store.Recent(context.Background(), time.Date(2026, 4, 22, 11, 0, 0, 0, time.UTC), 10)
	require.NoError(t, err)
	require.Len(t, recent, 2)
	assert.Equal(t, "delete_message", recent[0].Command)

	summary, err := store.Summary(context.Background(), time.Date(2026, 4, 22, 11, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, 2, summary.Total)
	assert.Equal(t, 1, summary.Completed)
	assert.Equal(t, 1, summary.Failed)
}

func TestModerationActionsRecentDetailedEnrichesOperationalContext(t *testing.T) {
	ctx := context.Background()
	db, err := engine.NewSqlite(":memory:", "gr1")
	require.NoError(t, err)
	defer db.Close()

	store, err := NewModerationActions(ctx, db)
	require.NoError(t, err)
	for _, schema := range []string{
		`CREATE TABLE incidents (tenant_id TEXT, idempotency_key TEXT, spam_user_id INTEGER,
			spam_user_name TEXT, message_text TEXT, reason_text TEXT, reason_code TEXT)`,
		`CREATE TABLE incoming_events (tenant_id TEXT, idempotency_key TEXT, message_id INTEGER,
			message_thread_id INTEGER, decision_reason TEXT)`,
		`CREATE TABLE community_rule_events (tenant_id TEXT, chat_id INTEGER, message_id INTEGER,
			user_id INTEGER, thread_id INTEGER, message_text TEXT, reason TEXT, rule_code TEXT)`,
		`CREATE TABLE user_messages (tenant_id TEXT, chat_id INTEGER, msg_id INTEGER,
			user_id INTEGER, user_name TEXT)`,
		`CREATE TABLE community_members (tenant_id TEXT, chat_id INTEGER, user_id INTEGER,
			username TEXT, display_name TEXT)`,
	} {
		_, err = db.Exec(schema)
		require.NoError(t, err)
	}
	tenantID := store.TenantID()
	_, err = db.Exec(`INSERT INTO incidents
		(tenant_id, idempotency_key, spam_user_id, spam_user_name, message_text, reason_text, reason_code)
		VALUES (?, 'key-1', 42, 'Alicia antigua', 'Mensaje literal', 'Insulto directo', 'llm_openai')`, tenantID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO incoming_events
		(tenant_id, idempotency_key, message_id, message_thread_id, decision_reason)
		VALUES (?, 'key-1', 77, 3, 'warning strike')`, tenantID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO user_messages
		(tenant_id, chat_id, msg_id, user_id, user_name) VALUES (?, 123, 77, 42, 'alicia_old')`, tenantID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO community_members
		(tenant_id, chat_id, user_id, username, display_name) VALUES (?, 123, 42, 'alicia', 'Alicia')`, tenantID)
	require.NoError(t, err)

	require.NoError(t, store.Add(ctx, ModerationActionEntry{
		EventID: "evt-1", IdempotencyKey: "key-1", Command: "mute_user", Status: "simulated",
		ChatID: 123, SubjectID: 42, MessageID: 0,
	}))
	require.NoError(t, store.Add(ctx, ModerationActionEntry{
		EventID: "evt-1", IdempotencyKey: "key-1", Command: "delete_message", Status: "failed",
		ChatID: 123, MessageID: 77, LastError: "message cannot be deleted",
	}))

	entries, err := store.RecentDetailed(ctx, time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	for _, entry := range entries {
		assert.Equal(t, int64(42), entry.TargetUserID)
		assert.Equal(t, "alicia", entry.UserName)
		assert.Equal(t, "Alicia", entry.DisplayName)
		assert.Equal(t, 77, entry.SourceMessageID)
		assert.Equal(t, 3, entry.ThreadID)
		assert.Equal(t, "Mensaje literal", entry.MessageText)
		assert.Equal(t, "Insulto directo", entry.Description)
		assert.Equal(t, "llm_openai", entry.ReasonCode)
	}
}

func TestModerationActionsLast(t *testing.T) {
	db, err := engine.NewSqlite(":memory:", "gr1")
	require.NoError(t, err)
	defer db.Close()

	store, err := NewModerationActions(context.Background(), db)
	require.NoError(t, err)

	err = store.Add(context.Background(), ModerationActionEntry{
		EventID:        "evt-1",
		CorrelationID:  "corr-1",
		IdempotencyKey: "key-1",
		Command:        "ban_user",
		Status:         "failed",
		ChatID:         123,
		SubjectID:      42,
		Attempt:        1,
		LastError:      "telegram timeout",
		CreatedAt:      time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	err = store.Add(context.Background(), ModerationActionEntry{
		EventID:        "evt-2",
		CorrelationID:  "corr-2",
		IdempotencyKey: "key-1",
		Command:        "ban_user",
		Status:         "completed",
		ChatID:         123,
		SubjectID:      42,
		Attempt:        2,
		CreatedAt:      time.Date(2026, 4, 22, 12, 1, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	replay, err := store.Last(context.Background(), ModerationActionLookup{
		IdempotencyKey: "key-1",
		Command:        "ban_user",
		ChatID:         123,
		SubjectID:      42,
	})
	require.NoError(t, err)
	assert.True(t, replay.Found)
	assert.True(t, replay.Completed)
	assert.Equal(t, 2, replay.Attempt)
	assert.Empty(t, replay.LastError)
}

func TestModerationActionsSimulatedCountsAsTerminal(t *testing.T) {
	db, err := engine.NewSqlite(":memory:", "gr1")
	require.NoError(t, err)
	defer db.Close()

	store, err := NewModerationActions(context.Background(), db)
	require.NoError(t, err)
	err = store.Add(context.Background(), ModerationActionEntry{
		EventID: "evt-sim", IdempotencyKey: "key-sim", Command: "warn_user",
		Status: "simulated", ChatID: 123, SubjectID: 42, MessageID: 77,
	})
	require.NoError(t, err)

	summary, err := store.Summary(context.Background(), time.Time{})
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Total)
	assert.Equal(t, 1, summary.Simulated)
	assert.Equal(t, 0, summary.Completed)
	assert.Equal(t, 0, summary.Failed)

	replay, err := store.Last(context.Background(), ModerationActionLookup{
		IdempotencyKey: "key-sim", Command: "warn_user",
		ChatID: 123, SubjectID: 42, MessageID: 77,
	})
	require.NoError(t, err)
	assert.True(t, replay.Found)
	assert.True(t, replay.Completed)
}

func TestModerationActionsMigrateFromOldSchema(t *testing.T) {
	db, err := engine.NewSqlite(":memory:", "gr1")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE TABLE IF NOT EXISTS moderation_actions (" +
		"id INTEGER PRIMARY KEY AUTOINCREMENT, " +
		"event_id TEXT NOT NULL, " +
		"correlation_id TEXT NOT NULL DEFAULT '', " +
		"idempotency_key TEXT NOT NULL DEFAULT '', " +
		"command TEXT NOT NULL, " +
		"status TEXT NOT NULL, " +
		"chat_id INTEGER NOT NULL DEFAULT 0, " +
		"subject_id INTEGER NOT NULL DEFAULT 0, " +
		"message_id INTEGER NOT NULL DEFAULT 0, " +
		"attempt INTEGER NOT NULL DEFAULT 1, " +
		"last_error TEXT DEFAULT '', " +
		"created_at DATETIME DEFAULT CURRENT_TIMESTAMP)")
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO moderation_actions
		(event_id, correlation_id, idempotency_key, command, status, chat_id, subject_id, attempt, created_at)
		VALUES ('evt-old', 'corr-old', 'key-old', 'ban_user', 'completed', 99, 88, 1, '2026-01-01T00:00:00Z')`)
	require.NoError(t, err)

	store, err := NewModerationActions(context.Background(), db)
	require.NoError(t, err)

	entries, err := store.ByEventID(context.Background(), "evt-old")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "gr1", entries[0].GID)
	assert.Equal(t, "evt-old", entries[0].EventID)
	assert.Equal(t, "ban_user", entries[0].Command)
}
