package events

import (
	"context"
	"testing"
	"time"

	tbapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/redstone-md/shield/app/events/mocks"
	"github.com/redstone-md/shield/app/moderation"
	"github.com/redstone-md/shield/app/observability"
)

type communityModeratorStub struct {
	input    CommunityMessage
	decision CommunityDecision
	err      error
}

func (s *communityModeratorStub) Evaluate(_ context.Context, msg CommunityMessage) (CommunityDecision, error) {
	s.input = msg
	return s.decision, s.err
}

func TestTransformPreservesForumAndAlbumMetadata(t *testing.T) {
	msg := transform(&tbapi.Message{
		MessageID:       55,
		MessageThreadID: 6,
		IsTopicMessage:  true,
		MediaGroupID:    "album-1",
		Chat:            tbapi.Chat{ID: -1001},
		From:            &tbapi.User{ID: 42},
		Photo:           []tbapi.PhotoSize{{FileID: "photo"}},
	})
	assert.Equal(t, 6, msg.MessageThreadID)
	assert.True(t, msg.IsTopicMessage)
	assert.Equal(t, "album-1", msg.MediaGroupID)

	listener := TelegramListener{TenantID: "sauvage"}
	event := listener.makeIncomingEvent(tbapi.Update{UpdateID: 99}, msg)
	assert.Equal(t, 6, event.MessageThreadID)
	assert.Equal(t, "album-1", event.MediaGroupID)
	assert.Equal(t, "6", event.Content.Attributes["message_thread_id"])
	assert.Equal(t, "album-1", event.Content.Attributes["media_group_id"])
}

func TestCommunityTopicRootReferenceIsNotAConversationReply(t *testing.T) {
	moderator := &communityModeratorStub{decision: CommunityDecision{
		Handled: true, Action: moderation.ActionAllow,
	}}
	listener := TelegramListener{TenantID: "sauvage", CommunityModerator: moderator}
	update := tbapi.Update{Message: &tbapi.Message{
		MessageID: 52642, MessageThreadID: 3, IsTopicMessage: true,
		MediaGroupID: "14283350186390044", Caption: "Somos una pareja joven.",
		Chat: tbapi.Chat{ID: -1001}, From: &tbapi.User{ID: 42},
		Photo:          []tbapi.PhotoSize{{FileID: "photo"}},
		ReplyToMessage: &tbapi.Message{MessageID: 3},
	}}

	handled, err := listener.handleCommunityMessage(context.Background(), update)
	require.NoError(t, err)
	assert.False(t, handled)
	assert.False(t, moderator.input.IsReply)
	assert.True(t, moderator.input.HasPhoto)
	assert.Equal(t, "Somos una pareja joven.", moderator.input.Text)
}

func TestCommunityRealTopicReplyRemainsConversation(t *testing.T) {
	moderator := &communityModeratorStub{decision: CommunityDecision{
		Handled: true, Action: moderation.ActionAllow,
	}}
	listener := TelegramListener{TenantID: "sauvage", CommunityModerator: moderator}
	update := tbapi.Update{Message: &tbapi.Message{
		MessageID: 52650, MessageThreadID: 3, IsTopicMessage: true,
		Text: "Bienvenidos", Chat: tbapi.Chat{ID: -1001}, From: &tbapi.User{ID: 42},
		ReplyToMessage: &tbapi.Message{MessageID: 52642},
	}}

	handled, err := listener.handleCommunityMessage(context.Background(), update)
	require.NoError(t, err)
	assert.False(t, handled)
	assert.True(t, moderator.input.IsReply)
}

func TestCommunityWarningDeletesAndRepliesInSameTopic(t *testing.T) {
	api := &mocks.TbAPIMock{
		RequestFunc: func(tbapi.Chattable) (*tbapi.APIResponse, error) {
			return &tbapi.APIResponse{Ok: true}, nil
		},
		SendFunc: func(tbapi.Chattable) (tbapi.Message, error) {
			return tbapi.Message{}, nil
		},
	}
	moderator := &communityModeratorStub{decision: CommunityDecision{
		Handled: true, Enforce: true, Action: moderation.ActionWarn,
		Rule: "contest_duplicate", Reason: "duplicate", UserMessage: "Solo una entrada.",
	}}
	listener := TelegramListener{TbAPI: api, TenantID: "sauvage", CommunityModerator: moderator}
	update := tbapi.Update{Message: &tbapi.Message{
		MessageID: 22, MessageThreadID: 6, MediaGroupID: "album-b",
		Chat: tbapi.Chat{ID: -1001}, From: &tbapi.User{ID: 42, UserName: "alice"},
		Photo: []tbapi.PhotoSize{{FileID: "photo"}},
	}}

	handled, err := listener.handleCommunityMessage(context.Background(), update)
	require.NoError(t, err)
	assert.True(t, handled)
	require.Len(t, api.RequestCalls(), 1)
	require.Len(t, api.SendCalls(), 1)
	warning, ok := api.SendCalls()[0].C.(tbapi.MessageConfig)
	require.True(t, ok)
	assert.Equal(t, 6, warning.MessageThreadID)
	assert.Equal(t, "Solo una entrada.", warning.Text)
	assert.Equal(t, "album-b", moderator.input.MediaGroupID)
}

func TestCommunityShadowDecisionDoesNotMutateTelegram(t *testing.T) {
	api := &mocks.TbAPIMock{}
	moderator := &communityModeratorStub{decision: CommunityDecision{
		Handled: true, Enforce: false, Action: moderation.ActionBan, Rule: "presentation_duplicate",
	}}
	listener := TelegramListener{TbAPI: api, TenantID: "sauvage", CommunityModerator: moderator}
	update := tbapi.Update{Message: &tbapi.Message{
		MessageID: 30, MessageThreadID: 3,
		Chat: tbapi.Chat{ID: -1001}, From: &tbapi.User{ID: 42},
	}}

	handled, err := listener.handleCommunityMessage(context.Background(), update)
	require.NoError(t, err)
	assert.False(t, handled)
	assert.Empty(t, api.RequestCalls())
	assert.Empty(t, api.SendCalls())
}

func TestCommunityUsesAuditedActionExecutor(t *testing.T) {
	moderator := &communityModeratorStub{decision: CommunityDecision{
		Handled: true, Enforce: true, Action: moderation.ActionRestrict,
		Rule: "contest_duplicate", Reason: "duplicate", UserMessage: "Solo una entrada.",
		Duration: time.Hour,
	}}
	actions := &actionExecutorSpy{}
	listener := TelegramListener{
		TenantID: "sauvage", CommunityModerator: moderator, ActionExecutor: actions,
		ModerationConfig: ModerationConfig{WarnDeleteDuration: time.Minute},
	}
	update := tbapi.Update{Message: &tbapi.Message{
		MessageID: 22, MessageThreadID: 6, Chat: tbapi.Chat{ID: -1001},
		From: &tbapi.User{ID: 42, UserName: "alice"}, Photo: []tbapi.PhotoSize{{FileID: "photo"}},
	}}

	handled, err := listener.handleCommunityMessage(context.Background(), update)
	require.NoError(t, err)
	assert.True(t, handled)
	require.Len(t, actions.deleteMessageCalls, 1)
	require.Len(t, actions.warnCalls, 1)
	require.Len(t, actions.banCalls, 1)
	assert.Equal(t, 6, actions.warnCalls[0].threadID)
	assert.Equal(t, time.Minute, actions.warnCalls[0].warnDelTime)
	assert.True(t, actions.banCalls[0].restrict)
	assert.Equal(t, time.Hour, actions.banCalls[0].duration)

	meta, ok := observability.MetadataFromContext(actions.warnCtxs[0])
	require.True(t, ok)
	assert.Equal(t, "community:sauvage:chat:-1001:message:22", meta.IdempotencyKey)
}

func TestCommunityGroupedContinuationOnlyDeletesTheRemainingAlbumItem(t *testing.T) {
	actions := &actionExecutorSpy{}
	moderator := &communityModeratorStub{decision: CommunityDecision{
		Handled: true, Enforce: true, GroupedContinuation: true,
		Action: moderation.ActionBan, Rule: "presentation_message_not_allowed",
		UserMessage: "No debe repetirse.",
	}}
	listener := TelegramListener{
		TenantID: "sauvage", CommunityModerator: moderator, ActionExecutor: actions,
	}
	update := tbapi.Update{Message: &tbapi.Message{
		MessageID: 52643, MessageThreadID: 3, MediaGroupID: "album-a",
		Chat: tbapi.Chat{ID: -1001}, From: &tbapi.User{ID: 42},
		Photo: []tbapi.PhotoSize{{FileID: "photo"}},
	}}

	handled, err := listener.handleCommunityMessage(context.Background(), update)
	require.NoError(t, err)
	assert.True(t, handled)
	require.Len(t, actions.deleteMessageCalls, 1)
	assert.Empty(t, actions.warnCalls)
	assert.Empty(t, actions.banCalls)
}
