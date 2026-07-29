package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/redstone-md/shield/app/moderation"
	"github.com/redstone-md/shield/app/storage/engine"
)

func TestIncomingEventsRecord(t *testing.T) {
	db, err := engine.NewSqlite(":memory:", "gr1")
	require.NoError(t, err)
	defer db.Close()

	store, err := NewIncomingEvents(context.Background(), db)
	require.NoError(t, err)

	event := moderation.IncomingEvent{
		EventID:         "evt-1",
		CorrelationID:   "corr-1",
		TenantID:        "tg-spam",
		Source:          "telegram.update",
		UpdateID:        701,
		ChatID:          123,
		MessageThreadID: 6,
		MediaGroupID:    "album-701",
		MessageID:       77,
		EditedMessageID: 0,
		IdempotencyKey:  "telegram:update:701:chat:123:message:77:edited:0",
		ReceivedAt:      time.Date(2026, 4, 13, 11, 0, 0, 0, time.UTC),
	}

	created, err := store.Record(context.Background(), event)
	require.NoError(t, err)
	assert.True(t, created)

	created, err = store.Record(context.Background(), event)
	require.NoError(t, err)
	assert.False(t, created)

	record, err := store.ByIdempotencyKey(context.Background(), event.IdempotencyKey)
	require.NoError(t, err)
	assert.Equal(t, "gr1", record.GID)
	assert.Equal(t, event.EventID, record.EventID)
	assert.Equal(t, event.CorrelationID, record.CorrelationID)
	assert.Equal(t, "gr1", record.TenantID)
	assert.Equal(t, event.UpdateID, record.UpdateID)
	assert.Equal(t, event.ChatID, record.ChatID)
	assert.Equal(t, event.MessageThreadID, record.MessageThreadID)
	assert.Equal(t, event.MediaGroupID, record.MediaGroupID)
	assert.Equal(t, event.MessageID, record.MessageID)
	assert.Equal(t, event.EditedMessageID, record.EditedMessageID)
	assert.Equal(t, event.IdempotencyKey, record.IdempotencyKey)
	assert.True(t, event.ReceivedAt.Equal(record.ReceivedAt), "received_at mismatch: want %v, got %v", event.ReceivedAt, record.ReceivedAt)
}

func TestIncomingEventsReserveReclaimsUnfinishedEvent(t *testing.T) {
	db, err := engine.NewSqlite(":memory:", "gr1")
	require.NoError(t, err)
	defer db.Close()

	store, err := NewIncomingEvents(context.Background(), db)
	require.NoError(t, err)
	event := moderation.IncomingEvent{
		EventID: "evt-reclaim", Source: "telegram.update", ChatID: 123,
		IdempotencyKey: "reclaim-key", ReceivedAt: time.Now().UTC(),
	}

	first, err := store.Reserve(context.Background(), event)
	require.NoError(t, err)
	assert.True(t, first.Recorded)

	reclaimed, err := store.Reserve(context.Background(), event)
	require.NoError(t, err)
	assert.True(t, reclaimed.Recorded)
	assert.False(t, reclaimed.Processed)
}

func TestIncomingEventsReserveAndComplete(t *testing.T) {
	db, err := engine.NewSqlite(":memory:", "gr1")
	require.NoError(t, err)
	defer db.Close()

	store, err := NewIncomingEvents(context.Background(), db)
	require.NoError(t, err)

	event := moderation.IncomingEvent{
		EventID:         "evt-2",
		CorrelationID:   "corr-2",
		TenantID:        "tg-spam",
		Source:          "telegram.update",
		UpdateID:        702,
		ChatID:          123,
		MessageID:       78,
		EditedMessageID: 0,
		IdempotencyKey:  "telegram:update:702:chat:123:message:78:edited:0",
		ReceivedAt:      time.Date(2026, 4, 13, 11, 5, 0, 0, time.UTC),
	}

	replay, err := store.Reserve(context.Background(), event)
	require.NoError(t, err)
	assert.True(t, replay.Recorded)
	assert.False(t, replay.Processed)

	err = store.Complete(context.Background(), event.IdempotencyKey,
		moderation.PolicyDecision{
			EventID:       event.EventID,
			CorrelationID: event.CorrelationID,
			Action:        moderation.ActionBan,
			Reason:        "smoke policy",
			Score:         1,
			DecidedAt:     time.Date(2026, 4, 13, 11, 6, 0, 0, time.UTC),
		},
		moderation.ModerationActionResult{
			EventID:       event.EventID,
			CorrelationID: event.CorrelationID,
			Action:        moderation.ActionBan,
			Applied:       true,
			Provider:      "telegram",
			AppliedAt:     time.Date(2026, 4, 13, 11, 6, 0, 0, time.UTC),
		},
	)
	require.NoError(t, err)

	replay, err = store.Reserve(context.Background(), event)
	require.NoError(t, err)
	assert.False(t, replay.Recorded)
	assert.True(t, replay.Processed)
	assert.Equal(t, moderation.ActionBan, replay.Decision.Action)
	assert.Equal(t, "smoke policy", replay.Decision.Reason)
	assert.True(t, replay.ActionResult.Applied)

	record, err := store.ByIdempotencyKey(context.Background(), event.IdempotencyKey)
	require.NoError(t, err)
	assert.True(t, record.ProcessedAt.Valid)
	assert.Equal(t, "ban", record.DecisionAction)
	assert.Equal(t, "smoke policy", record.DecisionReason)
	assert.True(t, record.ActionApplied.Valid)
	assert.True(t, record.ActionApplied.Bool)
}

func TestIncomingEventsReserveAllowsRetryAfterFailedAction(t *testing.T) {
	db, err := engine.NewSqlite(":memory:", "gr1")
	require.NoError(t, err)
	defer db.Close()

	store, err := NewIncomingEvents(context.Background(), db)
	require.NoError(t, err)

	event := moderation.IncomingEvent{
		EventID:        "evt-3",
		CorrelationID:  "corr-3",
		TenantID:       "tg-spam",
		Source:         "telegram.update",
		UpdateID:       703,
		ChatID:         123,
		MessageID:      79,
		IdempotencyKey: "telegram:update:703:chat:123:message:79:edited:0",
		ReceivedAt:     time.Date(2026, 4, 13, 11, 10, 0, 0, time.UTC),
	}

	replay, err := store.Reserve(context.Background(), event)
	require.NoError(t, err)
	assert.True(t, replay.Recorded)
	assert.False(t, replay.Processed)

	err = store.Complete(context.Background(), event.IdempotencyKey,
		moderation.PolicyDecision{
			EventID:       event.EventID,
			CorrelationID: event.CorrelationID,
			Action:        moderation.ActionBan,
			Reason:        "telegram failure",
			Score:         1,
			DecidedAt:     time.Date(2026, 4, 13, 11, 11, 0, 0, time.UTC),
		},
		moderation.ModerationActionResult{
			EventID:       event.EventID,
			CorrelationID: event.CorrelationID,
			Action:        moderation.ActionBan,
			Applied:       false,
			Provider:      "telegram",
			Error:         "telegram timeout",
			AppliedAt:     time.Date(2026, 4, 13, 11, 11, 0, 0, time.UTC),
		},
	)
	require.NoError(t, err)

	replay, err = store.Reserve(context.Background(), event)
	require.NoError(t, err)
	assert.True(t, replay.Recorded)
	assert.False(t, replay.Processed)

	record, err := store.ByIdempotencyKey(context.Background(), event.IdempotencyKey)
	require.NoError(t, err)
	assert.False(t, record.ProcessedAt.Valid)
	assert.Equal(t, "ban", record.DecisionAction)
	assert.Equal(t, "telegram timeout", record.ActionError)
}

func TestIncomingEventsSummary(t *testing.T) {
	db, err := engine.NewSqlite(":memory:", "gr1")
	require.NoError(t, err)
	defer db.Close()

	store, err := NewIncomingEvents(context.Background(), db)
	require.NoError(t, err)
	now := time.Now().UTC()

	for idx, action := range []moderation.Action{
		moderation.ActionAllow,
		moderation.ActionWarn,
		moderation.ActionBan,
	} {
		key := fmt.Sprintf("summary-%d", idx)
		_, err = store.Record(context.Background(), moderation.IncomingEvent{
			EventID: key, Source: "telegram.update", ChatID: 123,
			MessageID: idx + 1, IdempotencyKey: key, ReceivedAt: now,
		})
		require.NoError(t, err)
		err = store.Complete(context.Background(), key,
			moderation.PolicyDecision{Action: action, DecidedAt: now},
			moderation.ModerationActionResult{Action: action, Applied: true, AppliedAt: now},
		)
		require.NoError(t, err)
	}
	_, err = store.Record(context.Background(), moderation.IncomingEvent{
		EventID: "pending", Source: "telegram.update", ChatID: 123,
		MessageID: 4, IdempotencyKey: "pending", ReceivedAt: now,
	})
	require.NoError(t, err)

	summary, err := store.Summary(context.Background(), now.Add(-time.Minute))
	require.NoError(t, err)
	assert.Equal(t, 4, summary.Total)
	assert.Equal(t, 3, summary.Processed)
	assert.Equal(t, 1, summary.Pending)
	assert.Equal(t, 1, summary.Allowed)
	assert.Equal(t, 1, summary.Warned)
	assert.Equal(t, 1, summary.Banned)
}
