package community

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	tbapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/redstone-md/shield/app/events"
	"github.com/redstone-md/shield/app/moderation"
)

type contestTelegramStub struct {
	created, closed, deleted, pinned int
}

func (s *contestTelegramStub) Send(c tbapi.Chattable) (tbapi.Message, error) {
	return tbapi.Message{MessageID: 501}, nil
}

func (s *contestTelegramStub) Request(c tbapi.Chattable) (*tbapi.APIResponse, error) {
	switch c.(type) {
	case tbapi.CreateForumTopicConfig:
		s.created++
		payload, _ := json.Marshal(tbapi.ForumTopic{MessageThreadID: 88, Name: "Fotos"})
		return &tbapi.APIResponse{Ok: true, Result: payload}, nil
	case tbapi.CloseForumTopicConfig:
		s.closed++
	case tbapi.DeleteForumTopicConfig:
		s.deleted++
	case tbapi.PinChatMessageConfig:
		s.pinned++
	}
	return &tbapi.APIResponse{Ok: true}, nil
}

func TestDynamicContestEnforcesOnePhotoWithoutStrikesAndSupportsAppeal(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(ctx, newTestDB(t))
	require.NoError(t, err)
	draft, err := store.CreateContestDraft(ctx, ContestDraftInput{Title: "Retratos", Bases: "Una foto original"}, -1001)
	require.NoError(t, err)
	_, err = store.setContestPublishing(ctx, draft.ContestID)
	require.NoError(t, err)
	require.NoError(t, store.markContestActive(ctx, draft.ContestID, 55, 100))

	eng, err := NewEngine(store, Config{
		Enabled: true, ChatID: -1001, PresentationThreadID: 3, ContestThreadID: 6, ContestID: "legacy", Shadow: true,
	})
	require.NoError(t, err)
	first, err := eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 55, MessageID: 101, UserID: 42,
		UserName: "ana", DisplayName: "Ana", HasPhoto: true, PhotoFileID: "photo-1",
	})
	require.NoError(t, err)
	assert.Equal(t, moderation.ActionAllow, first.Action)

	duplicate, err := eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 55, MessageID: 102, UserID: 42,
		UserName: "ana", DisplayName: "Ana", HasPhoto: true, PhotoFileID: "photo-2", Text: "Mi otra foto",
	})
	require.NoError(t, err)
	assert.Equal(t, "contest_duplicate", duplicate.Rule)
	assert.Equal(t, moderation.ActionWarn, duplicate.Action)
	assert.True(t, duplicate.Enforce)
	assert.True(t, duplicate.ContestAction)
	assert.NotEmpty(t, duplicate.AppealToken)
	assert.Equal(t, 101, duplicate.RelatedMessageID)

	album, err := eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 55, MessageID: 103, UserID: 77,
		HasPhoto: true, MediaGroupID: "album-not-allowed",
	})
	require.NoError(t, err)
	assert.Equal(t, "contest_format", album.Rule)
	assert.True(t, album.ContestAction)

	var strikes int
	require.NoError(t, store.db.GetContext(ctx, &strikes, store.db.Adopt(
		`SELECT COUNT(*) FROM community_violations WHERE tenant_id = ? AND chat_id = ?`), "sauvage", int64(-1001)))
	assert.Zero(t, strikes, "contest rejections must never escalate to restrict or ban")

	require.NoError(t, store.OpenContestAppeal(ctx, duplicate.AppealToken, 42, "La primera foto era incorrecta"))
	appeal, err := store.ResolveContestAppeal(ctx, duplicate.AppealToken, true, "Puedes volver a participar")
	require.NoError(t, err)
	assert.Equal(t, "accepted", appeal.Status)

	replacement, err := eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 55, MessageID: 104, UserID: 42, HasPhoto: true,
	})
	require.NoError(t, err)
	assert.Equal(t, moderation.ActionAllow, replacement.Action)
}

func TestContestLeaderboardUsesAggregateReactionSnapshots(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(ctx, newTestDB(t))
	require.NoError(t, err)
	draft, err := store.CreateContestDraft(ctx, ContestDraftInput{Title: "Fotos", Bases: "Bases"}, -1001)
	require.NoError(t, err)
	_, err = store.setContestPublishing(ctx, draft.ContestID)
	require.NoError(t, err)
	require.NoError(t, store.markContestActive(ctx, draft.ContestID, 77, 900))
	now := time.Now().UTC()

	for _, msg := range []events.CommunityMessage{
		{TenantID: "sauvage", ChatID: -1001, ThreadID: 77, MessageID: 901, UserID: 1, UserName: "uno", HasPhoto: true, ReceivedAt: now},
		{TenantID: "sauvage", ChatID: -1001, ThreadID: 77, MessageID: 902, UserID: 2, UserName: "dos", HasPhoto: true, ReceivedAt: now},
	} {
		claimed, _, _, claimErr := store.claimContest(ctx, msg, draft.ContestID, domainEntryKey(msg))
		require.NoError(t, claimErr)
		require.True(t, claimed)
		require.NoError(t, store.observeMember(ctx, msg))
	}
	require.NoError(t, store.RecordReactionCounts(ctx, -1001, 901, now, map[string]int{"❤️": 2, "🔥": 3}))
	require.NoError(t, store.RecordReactionCounts(ctx, -1001, 902, now, map[string]int{"❤️": 4}))

	leaders, err := store.ContestLeaderboard(ctx, draft.ContestID)
	require.NoError(t, err)
	require.Len(t, leaders, 2)
	assert.Equal(t, int64(1), leaders[0].UserID)
	assert.Equal(t, 5, leaders[0].ReactionTotal)
	assert.Equal(t, 3, leaders[0].Breakdown["🔥"])
	assert.Equal(t, 4, leaders[1].ReactionTotal)
}

func TestContestManagerPublishesAndDeletesOnlyAfterSecondPhase(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(ctx, newTestDB(t))
	require.NoError(t, err)
	telegram := &contestTelegramStub{}
	manager := NewContestManager(store, telegram, -1001)
	manager.settleDelay = 0
	draft, err := manager.CreateDraft(ctx, ContestDraftInput{Title: "Fotos", Bases: "Una foto"})
	require.NoError(t, err)
	require.NoError(t, manager.UpdateDraft(ctx, draft.ContestID, draft.AnnouncementText+"\nTexto revisado"))

	active, err := manager.Publish(ctx, draft.ContestID)
	require.NoError(t, err)
	assert.Equal(t, ContestActive, active.Status)
	assert.Equal(t, 88, active.ThreadID)
	assert.Equal(t, 1, telegram.created)
	assert.Equal(t, 1, telegram.pinned)
	assert.Zero(t, telegram.deleted)

	closing, err := manager.BeginFinalize(ctx, draft.ContestID)
	require.NoError(t, err)
	assert.Equal(t, ContestClosing, closing.Status)
	assert.Equal(t, 1, telegram.closed)
	assert.Zero(t, telegram.deleted, "first phase must not delete the conversation")

	finalized, err := manager.ConfirmFinalize(ctx, draft.ContestID)
	require.NoError(t, err)
	assert.Equal(t, ContestFinalized, finalized.Status)
	assert.Equal(t, 1, telegram.deleted)
}
