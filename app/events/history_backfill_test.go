package events

import (
	"context"
	"testing"
	"time"

	tbapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/redstone-md/shield/app/moderation"
)

type historicalProcessorSpy struct {
	events  []moderation.IncomingEvent
	updates []tbapi.Update
}

func (s *historicalProcessorSpy) Process(
	_ context.Context, event moderation.IncomingEvent, update tbapi.Update,
) error {
	s.events = append(s.events, event)
	s.updates = append(s.updates, update)
	return nil
}

type historicalCommunitySpy struct {
	messages []CommunityMessage
}

func (s *historicalCommunitySpy) Evaluate(
	_ context.Context, msg CommunityMessage,
) (CommunityDecision, error) {
	s.messages = append(s.messages, msg)
	return CommunityDecision{Handled: true, Action: moderation.ActionAllow}, nil
}

func TestAnalyzeHistoricalMessagesRequiresPassiveMode(t *testing.T) {
	listener := TelegramListener{}
	_, err := listener.AnalyzeHistoricalMessages(context.Background(), []HistoricalMessage{{
		IdempotencyKey: "archive:1",
		ChatID:         -1001,
		MessageID:      1,
		UserID:         2,
		ReceivedAt:     time.Now(),
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires dry or training mode")
}

func TestAnalyzeHistoricalMessagesUsesCommunityAndNormalPipelineWithoutMediaDownload(t *testing.T) {
	processor := &historicalProcessorSpy{}
	community := &historicalCommunitySpy{}
	listener := TelegramListener{
		Dry:                true,
		TenantID:           "sauvage",
		processor:          processor,
		CommunityModerator: community,
	}
	receivedAt := time.Date(2026, 7, 24, 16, 24, 20, 0, time.UTC)

	summary, err := listener.AnalyzeHistoricalMessages(context.Background(), []HistoricalMessage{{
		IdempotencyKey: "telegram:archive:personal:message-51183",
		ChatID:         -1003672565710,
		ThreadID:       3,
		MessageID:      51183,
		MediaGroupID:   "archive:3:42:51183",
		UserID:         42,
		UserName:       "Alice",
		Text:           "Acepto privados",
		HasPhoto:       true,
		ReceivedAt:     receivedAt,
	}})
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Processed)

	require.Len(t, community.messages, 1)
	assert.True(t, community.messages[0].HasPhoto)
	assert.Equal(t, "archive:3:42:51183", community.messages[0].MediaGroupID)

	require.Len(t, processor.events, 1)
	event := processor.events[0]
	assert.Equal(t, "telegram.archive", event.Source)
	assert.Equal(t, "telegram:archive:personal:message-51183", event.IdempotencyKey)
	assert.True(t, event.Content.HasMedia)
	assert.Equal(t, "true", event.Content.Attributes["historical"])
	assert.Equal(t, receivedAt, event.ReceivedAt)

	require.Len(t, processor.updates, 1)
	assert.Empty(t, processor.updates[0].Message.Photo)
	assert.Equal(t, "Acepto privados", processor.updates[0].Message.Text)
}

func TestAnalyzeHistoricalMessagesValidatesStableIdentity(t *testing.T) {
	listener := TelegramListener{Dry: true}
	_, err := listener.AnalyzeHistoricalMessages(context.Background(), []HistoricalMessage{{
		ChatID:     -1001,
		MessageID:  1,
		UserID:     2,
		ReceivedAt: time.Now(),
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "idempotency key is required")
}
