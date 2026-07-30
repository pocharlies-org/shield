package community

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/redstone-md/shield/app/events"
	"github.com/redstone-md/shield/app/moderation"
	"github.com/redstone-md/shield/app/storage/engine"
)

func TestPresentationIsPersistentAndAlbumAware(t *testing.T) {
	eng := newTestEngine(t, Config{
		Enabled: true, ChatID: -1001, PresentationThreadID: 3, ContestThreadID: 6,
		ContestID: "summer-2026", RequirePresentationText: true,
	})
	ctx := context.Background()

	first, err := eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 10,
		MediaGroupID: "album-a", UserID: 42, HasPhoto: true,
	})
	require.NoError(t, err)
	assert.True(t, first.Handled)
	assert.Equal(t, moderation.ActionAllow, first.Action)

	partTwo, err := eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 11,
		MediaGroupID: "album-a", UserID: 42, HasPhoto: true,
	})
	require.NoError(t, err)
	assert.Equal(t, moderation.ActionAllow, partTwo.Action)

	duplicate, err := eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 12,
		UserID: 42, HasPhoto: true, Text: "Hola, acepto privados",
	})
	require.NoError(t, err)
	assert.Equal(t, moderation.ActionWarn, duplicate.Action)
	assert.True(t, duplicate.Enforce)
	assert.Equal(t, "presentation_duplicate", duplicate.Rule)
}

func TestPresentationSeparatesConversationIncompleteAndValidPosts(t *testing.T) {
	eng := newTestEngine(t, Config{
		Enabled: true, ChatID: -1001, PresentationThreadID: 3, ContestThreadID: 6,
		ContestID: "summer-2026", RequirePresentationText: true,
	})

	conversation, err := eng.Evaluate(context.Background(), events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 1,
		UserID: 1, Text: "¿Y eso?", IsReply: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "presentation_message_not_allowed", conversation.Rule)

	empty, err := eng.Evaluate(context.Background(), events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 2,
		UserID: 2, HasPhoto: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "presentation_incomplete", empty.Rule)

	valid, err := eng.Evaluate(context.Background(), events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 3,
		UserID: 3, HasPhoto: true, Text: "Hola, soy Ana",
	})
	require.NoError(t, err)
	assert.Equal(t, moderation.ActionAllow, valid.Action)
}

func TestPresentationRegressionForRecentSauvageMessages(t *testing.T) {
	eng := newTestEngine(t, Config{
		Enabled: true, ChatID: -1001, PresentationThreadID: 3, ContestThreadID: 6,
		ContestID: "summer-2026", RequirePresentationText: true, Shadow: true,
	})
	ctx := context.Background()

	presentation, err := eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 52425,
		UserID: 1, HasPhoto: true, Text: "Miquel 29 y Ariadna 27 aceptamos privados de todo el mundo",
	})
	require.NoError(t, err)
	assert.Equal(t, "presentation_once", presentation.Rule)

	reply, err := eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 52504,
		UserID: 2, IsReply: true, Text: "¿Y eso?",
	})
	require.NoError(t, err)
	assert.Equal(t, "presentation_message_not_allowed", reply.Rule)
	assert.Contains(t, reply.Reason, "Se ha enviado un mensaje")

	duplicate, err := eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 52508,
		UserID: 1, HasPhoto: true, Text: "Otra presentación",
	})
	require.NoError(t, err)
	assert.Equal(t, "presentation_duplicate", duplicate.Rule)
}

func TestContestAllowsOneEntryAndRotatesByContestID(t *testing.T) {
	db := newTestDB(t)
	store, err := NewStore(context.Background(), db)
	require.NoError(t, err)

	config := Config{
		Enabled: true, ChatID: -1001, PresentationThreadID: 3, ContestThreadID: 6,
		ContestID: "contest-a",
	}
	eng, err := NewEngine(store, config)
	require.NoError(t, err)

	first, err := eng.Evaluate(context.Background(), events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 6, MessageID: 20,
		MediaGroupID: "entry-a", UserID: 77, HasPhoto: true,
	})
	require.NoError(t, err)
	assert.Equal(t, moderation.ActionAllow, first.Action)

	second, err := eng.Evaluate(context.Background(), events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 6, MessageID: 21,
		MediaGroupID: "entry-b", UserID: 77, HasPhoto: true,
	})
	require.NoError(t, err)
	assert.Equal(t, moderation.ActionWarn, second.Action)
	assert.Equal(t, "contest_duplicate", second.Rule)

	config.ContestID = "contest-b"
	nextContest, err := NewEngine(store, config)
	require.NoError(t, err)
	rotated, err := nextContest.Evaluate(context.Background(), events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 6, MessageID: 22,
		UserID: 77, HasPhoto: true,
	})
	require.NoError(t, err)
	assert.Equal(t, moderation.ActionAllow, rotated.Action)
}

func TestViolationsEscalateAndShadowDoesNotEnforce(t *testing.T) {
	eng := newTestEngine(t, Config{
		Enabled: true, ChatID: -1001, PresentationThreadID: 3, ContestThreadID: 6,
		ContestID: "summer-2026",
	})
	ctx := context.Background()
	actions := make([]moderation.Action, 0, 4)
	for id := 1; id <= 4; id++ {
		decision, err := eng.Evaluate(ctx, events.CommunityMessage{
			TenantID: "sauvage", ChatID: -1001, ThreadID: 6, MessageID: id,
			UserID: 91, Text: "comentario sin foto",
		})
		require.NoError(t, err)
		actions = append(actions, decision.Action)
	}
	assert.Equal(t, []moderation.Action{
		moderation.ActionWarn,
		moderation.ActionRestrict,
		moderation.ActionRestrict,
		moderation.ActionBan,
	}, actions)

	shadow := newTestEngine(t, Config{
		Enabled: true, ChatID: -2002, PresentationThreadID: 3, ContestThreadID: 6,
		ContestID: "shadow", Shadow: true,
	})
	decision, err := shadow.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -2002, ThreadID: 6, MessageID: 100,
		UserID: 92, Text: "comentario sin foto",
	})
	require.NoError(t, err)
	assert.Equal(t, moderation.ActionWarn, decision.Action)
	assert.False(t, decision.Enforce)
}

func TestAlbumViolationCountsOnceAndCollapsesToOneDashboardEvent(t *testing.T) {
	eng := newTestEngine(t, Config{
		Enabled: true, ChatID: -1001, PresentationThreadID: 3, ContestThreadID: 6,
		ContestID: "summer-2026",
	})
	ctx := context.Background()
	first, err := eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 100,
		MediaGroupID: "album-reply", UserID: 91, HasPhoto: true, IsReply: true,
		Text: "Mensaje literal del álbum",
	})
	require.NoError(t, err)
	assert.Equal(t, moderation.ActionWarn, first.Action)
	assert.False(t, first.GroupedContinuation)

	second, err := eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 101,
		MediaGroupID: "album-reply", UserID: 91, HasPhoto: true, IsReply: true,
	})
	require.NoError(t, err)
	assert.Equal(t, moderation.ActionWarn, second.Action)
	assert.True(t, second.GroupedContinuation)

	eventsList, err := eng.store.ListRuleEvents(ctx, RuleEventFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, eventsList, 1)
	assert.Equal(t, 100, eventsList[0].MessageID)
	assert.Equal(t, "Mensaje literal del álbum", eventsList[0].MessageText)
	assert.Equal(t, "album-reply", eventsList[0].MediaGroupID)
	assert.Equal(t, 1, eventsList[0].GroupSize)

	next, err := eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 102,
		UserID: 91, Text: "otra conversación",
	})
	require.NoError(t, err)
	assert.Equal(t, moderation.ActionRestrict, next.Action)
}

func TestShadowViolationsDoNotPreloadLiveStrikes(t *testing.T) {
	db := newTestDB(t)
	store, err := NewStore(context.Background(), db)
	require.NoError(t, err)
	config := Config{
		Enabled: true, ChatID: -1001, PresentationThreadID: 3, ContestThreadID: 6,
		ContestID: "summer-2026", Shadow: true,
	}
	shadow, err := NewEngine(store, config)
	require.NoError(t, err)
	for id := 1; id <= 4; id++ {
		decision, evalErr := shadow.Evaluate(context.Background(), events.CommunityMessage{
			TenantID: "sauvage", ChatID: -1001, ThreadID: 6, MessageID: id,
			UserID: 91, Text: "comentario sin foto",
		})
		require.NoError(t, evalErr)
		assert.False(t, decision.Enforce)
	}

	config.Shadow = false
	live, err := NewEngine(store, config)
	require.NoError(t, err)
	decision, err := live.Evaluate(context.Background(), events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 6, MessageID: 5,
		UserID: 91, Text: "comentario sin foto",
	})
	require.NoError(t, err)
	assert.Equal(t, moderation.ActionWarn, decision.Action)
	assert.True(t, decision.Enforce)
}

func TestRedeliveredViolationDoesNotAddAnotherStrike(t *testing.T) {
	eng := newTestEngine(t, Config{
		Enabled: true, ChatID: -1001, PresentationThreadID: 3, ContestThreadID: 6,
		ContestID: "summer-2026",
	})
	msg := events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 6, MessageID: 1,
		UserID: 91, Text: "comentario sin foto",
	}

	first, err := eng.Evaluate(context.Background(), msg)
	require.NoError(t, err)
	replay, err := eng.Evaluate(context.Background(), msg)
	require.NoError(t, err)
	assert.Equal(t, moderation.ActionWarn, first.Action)
	assert.Equal(t, first, replay)

	msg.MessageID = 2
	next, err := eng.Evaluate(context.Background(), msg)
	require.NoError(t, err)
	assert.Equal(t, moderation.ActionRestrict, next.Action)
	assert.Equal(t, time.Hour, next.Duration)
}

func TestConcurrentPresentationClaimHasOneWinner(t *testing.T) {
	eng := newTestEngine(t, Config{
		Enabled: true, ChatID: -1001, PresentationThreadID: 3, ContestThreadID: 6,
		ContestID: "summer-2026",
	})

	var wg sync.WaitGroup
	decisions := make(chan events.CommunityDecision, 8)
	errs := make(chan error, 8)
	for id := 1; id <= 8; id++ {
		wg.Add(1)
		go func(messageID int) {
			defer wg.Done()
			decision, err := eng.Evaluate(context.Background(), events.CommunityMessage{
				TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: messageID,
				UserID: 500, HasPhoto: true,
			})
			errs <- err
			decisions <- decision
		}(id)
	}
	wg.Wait()
	close(errs)
	close(decisions)

	allowed := 0
	for err := range errs {
		require.NoError(t, err)
	}
	for decision := range decisions {
		if decision.Action == moderation.ActionAllow {
			allowed++
		}
	}
	assert.Equal(t, 1, allowed)
}

func TestUnmanagedTopicAndAdministratorBypass(t *testing.T) {
	eng := newTestEngine(t, Config{
		Enabled: true, ChatID: -1001, PresentationThreadID: 3, ContestThreadID: 6,
		ContestID: "summer-2026",
	})
	unmanaged, err := eng.Evaluate(context.Background(), events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 1, MessageID: 1, UserID: 1,
	})
	require.NoError(t, err)
	assert.False(t, unmanaged.Handled)

	admin, err := eng.Evaluate(context.Background(), events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 6, MessageID: 2,
		UserID: 1, IsAdministrator: true,
	})
	require.NoError(t, err)
	assert.True(t, admin.Handled)
	assert.Equal(t, moderation.ActionAllow, admin.Action)
}

func newTestEngine(t *testing.T, config Config) *Engine {
	t.Helper()
	store, err := NewStore(context.Background(), newTestDB(t))
	require.NoError(t, err)
	eng, err := NewEngine(store, config)
	require.NoError(t, err)
	return eng
}

func newTestDB(t *testing.T) *engine.SQL {
	t.Helper()
	db, err := engine.NewSqlite(filepath.Join(t.TempDir(), "community.db"), "sauvage")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}
