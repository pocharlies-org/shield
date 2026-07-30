package community

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/redstone-md/shield/app/events"
)

func TestMemberDirectoryTracksIdentityPresentationWarningsAndReports(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(ctx, newTestDB(t))
	require.NoError(t, err)
	eng, err := NewEngine(store, Config{
		Enabled: true, ChatID: -1001, PresentationThreadID: 3, ContestThreadID: 6,
		ContestID: "summer", Shadow: true, RequirePresentationText: true,
	})
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	_, err = eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 10, UserID: 42,
		UserName: "ana", DisplayName: "Ana Real", HasPhoto: true, Text: "Hola, soy Ana", ReceivedAt: now,
	})
	require.NoError(t, err)
	_, err = eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 11, UserID: 42,
		UserName: "ana", DisplayName: "Ana Real", IsReply: true, Text: "gracias", ReceivedAt: now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.NoError(t, store.CreateCommunityUserReport(ctx, events.CommunityUserReport{
		ReportKey: "report-1", ChatID: -1001,
		ReporterUserID: 7, ReporterUserName: "reporter",
		ReportedUserID: 42, ReportedUserName: "ana", ReportedDisplayName: "Ana Real",
		Reason: "Me ha escrito repetidamente", CreatedAt: now.Add(2 * time.Minute),
	}))

	members, err := store.ListMembers(ctx, MemberFilter{ChatID: -1001, Query: "@ana", Limit: 10})
	require.NoError(t, err)
	require.Len(t, members, 1)
	assert.Equal(t, "Ana Real", members[0].DisplayName)
	assert.True(t, members[0].HasPresentation)
	assert.Equal(t, 10, members[0].PresentationMessageID)
	assert.Equal(t, 11, members[0].LastMessageID)
	assert.Equal(t, 1, members[0].WarningCount)
	assert.Equal(t, 1, members[0].ReportCount)

	reports, err := store.ListUserReports(ctx, UserReportFilter{ReportedUserID: 42, Limit: 10})
	require.NoError(t, err)
	require.Len(t, reports, 1)
	assert.Equal(t, "Me ha escrito repetidamente", reports[0].Reason)
}

func TestOlderMemberObservationDoesNotRegressLastMessage(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(ctx, newTestDB(t))
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, store.ObserveMember(ctx, events.CommunityMember{
		ChatID: -1001, UserID: 9, UserName: "newname", DisplayName: "Nombre",
		LastMessageID: 50, LastThreadID: 2, LastMessageAt: now,
	}))
	require.NoError(t, store.ObserveMember(ctx, events.CommunityMember{
		ChatID: -1001, UserID: 9, UserName: "", DisplayName: "",
		LastMessageID: 10, LastThreadID: 1, LastMessageAt: now.Add(-time.Hour),
	}))
	member, found, err := store.GetCommunityMember(ctx, -1001, 9)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, 50, member.LastMessageID)
	assert.Equal(t, 2, member.LastThreadID)
	assert.Equal(t, "newname", member.UserName)
	assert.Equal(t, "Nombre", member.DisplayName)
}

func TestUpdateMemberIdentityDoesNotChangeLastMessageMetadata(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(ctx, newTestDB(t))
	require.NoError(t, err)
	observedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, store.ObserveMember(ctx, events.CommunityMember{
		ChatID: -1001, UserID: 9, DisplayName: "Nombre antiguo",
		LastMessageID: 50, LastThreadID: 3, LastMessageAt: observedAt,
	}))

	require.NoError(t, store.UpdateMemberIdentity(ctx, -1001, 9, "@usuario_actual", "Nombre Actual"))

	member, found, err := store.GetCommunityMember(ctx, -1001, 9)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "usuario_actual", member.UserName)
	assert.Equal(t, "Nombre Actual", member.DisplayName)
	assert.Equal(t, 50, member.LastMessageID)
	assert.Equal(t, 3, member.LastThreadID)
	assert.True(t, observedAt.Equal(member.LastMessageAt))
}

func TestResetHistoricalTopicStateIsLimitedToShadowLookback(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(ctx, newTestDB(t))
	require.NoError(t, err)
	eng, err := NewEngine(store, Config{
		Enabled: true, ChatID: -1001, PresentationThreadID: 3, ContestThreadID: 6,
		ContestID: "summer", Shadow: true,
	})
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 1, UserID: 1,
		HasPhoto: true, MediaGroupID: "old", ReceivedAt: now.Add(-10 * 24 * time.Hour),
	})
	require.NoError(t, err)
	_, err = eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 2, UserID: 2,
		HasPhoto: true, MediaGroupID: "recent", ReceivedAt: now.Add(-time.Hour),
	})
	require.NoError(t, err)

	require.NoError(t, store.ResetHistoricalTopicState(ctx, -1001, []int{3}, now.Add(-7*24*time.Hour)))
	presentations, err := store.ListPresentations(ctx, 10)
	require.NoError(t, err)
	require.Len(t, presentations, 1)
	assert.Equal(t, int64(1), presentations[0].UserID)
	eventsAfter, err := store.ListRuleEvents(ctx, RuleEventFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, eventsAfter, 1)
	assert.Equal(t, 1, eventsAfter[0].MessageID)
}
