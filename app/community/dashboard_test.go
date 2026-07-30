package community

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/redstone-md/shield/app/events"
)

func TestDashboardSummarizesAndFiltersCommunityData(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(ctx, newTestDB(t))
	require.NoError(t, err)
	config := Config{
		Enabled: true, ChatID: -1001, PresentationThreadID: 3, ContestThreadID: 6,
		ContestID: "summer-2026", Shadow: true,
	}
	engine, err := NewEngine(store, config)
	require.NoError(t, err)

	now := time.Now().UTC()
	_, err = engine.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 1, UserID: 10,
		HasPhoto: true, ReceivedAt: now,
	})
	require.NoError(t, err)
	_, err = engine.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 2, UserID: 10,
		HasPhoto: true, Text: "Presentación repetida literal", ReceivedAt: now,
	})
	require.NoError(t, err)
	_, err = engine.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 6, MessageID: 3, UserID: 11,
		HasPhoto: true, ReceivedAt: now,
	})
	require.NoError(t, err)

	snapshot, err := store.Dashboard(ctx, now.Add(-time.Hour), 10)
	require.NoError(t, err)
	assert.Equal(t, 3, snapshot.Summary.TotalEvents)
	assert.Equal(t, 2, snapshot.Summary.Allowed)
	assert.Equal(t, 1, snapshot.Summary.ShadowViolations)
	assert.Equal(t, 1, snapshot.Summary.Presentations)
	assert.Equal(t, 1, snapshot.Summary.ContestEntries)
	require.Len(t, snapshot.RecentEvents, 3)
	require.Len(t, snapshot.Daily, 1)
	assert.Equal(t, now.Format("2006-01-02"), snapshot.Daily[0].Day)
	assert.Equal(t, 1, snapshot.Daily[0].Count)

	shadow := true
	filtered, err := store.ListRuleEvents(ctx, RuleEventFilter{
		UserID: 10, Shadow: &shadow, Action: "warn", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, "presentation_duplicate", filtered[0].RuleCode)
	assert.Equal(t, "Presentación repetida literal", filtered[0].MessageText)
}

func TestDashboardCollapsesLegacyAlbumRowsIntoOneLogicalDecision(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(ctx, newTestDB(t))
	require.NoError(t, err)
	now := time.Now().UTC()
	insert := store.db.Adopt(`INSERT INTO community_rule_events
		(tenant_id, event_key, chat_id, thread_id, message_id, media_group_id, user_id,
		 rule_code, action, reason, message_text, user_message, duration_seconds, shadow, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	actions := []string{"warn", "restrict", "restrict", "ban", "ban", "ban"}
	for idx, action := range actions {
		messageID := 52642 + idx
		messageText := ""
		if idx == 0 {
			messageText = "Presentación literal del álbum"
		}
		_, err = store.db.ExecContext(ctx, insert,
			"sauvage", fmt.Sprintf("legacy:%d", messageID), int64(-1001), 3, messageID,
			"14283350186390044", int64(42), "presentation_message_not_allowed", action,
			"motivo antiguo", messageText, "aviso", int64(0), true, now,
		)
		require.NoError(t, err)
	}

	eventsList, err := store.ListRuleEvents(ctx, RuleEventFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, eventsList, 1)
	assert.Equal(t, 52642, eventsList[0].MessageID)
	assert.Equal(t, 6, eventsList[0].GroupSize)
	assert.Equal(t, "warn", eventsList[0].Action)
	assert.Equal(t, "Presentación literal del álbum", eventsList[0].MessageText)

	snapshot, err := store.Dashboard(ctx, now.Add(-time.Hour), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, snapshot.Summary.TotalEvents)
	assert.Equal(t, 1, snapshot.Summary.Warnings)
	assert.Zero(t, snapshot.Summary.Restrictions)
	assert.Zero(t, snapshot.Summary.Bans)
}

func TestRuleEventJSONDoesNotExposeRawMessageEvidence(t *testing.T) {
	payload, err := json.Marshal(RuleEvent{
		EventKey: "event", MessageText: "mensaje privado", MediaGroupID: "album-private", GroupSize: 6,
	})
	require.NoError(t, err)
	assert.NotContains(t, string(payload), "mensaje privado")
	assert.NotContains(t, string(payload), "album-private")
	assert.NotContains(t, string(payload), "GroupSize")
}
