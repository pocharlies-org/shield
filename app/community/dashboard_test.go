package community

import (
	"context"
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
		HasPhoto: true, ReceivedAt: now,
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
}
