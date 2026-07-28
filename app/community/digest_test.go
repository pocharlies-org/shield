package community

import (
	"context"
	"strings"
	"testing"

	tbapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/stretchr/testify/require"

	"github.com/redstone-md/shield/app/audit"
	"github.com/redstone-md/shield/app/events"
	eventmocks "github.com/redstone-md/shield/app/events/mocks"
	"github.com/redstone-md/shield/app/storage"
)

func TestDigestIncludesCommunityIncidentAndActionCounts(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	communityStore, err := NewStore(ctx, db)
	require.NoError(t, err)
	actionStore, err := storage.NewModerationActions(ctx, db)
	require.NoError(t, err)
	incidentStore, err := storage.NewIncidentStorage(ctx, db)
	require.NoError(t, err)

	eng, err := NewEngine(communityStore, Config{
		Enabled: true, ChatID: -1001, PresentationThreadID: 3, ContestThreadID: 6,
		ContestID: "summer-2026", Shadow: true,
	})
	require.NoError(t, err)
	_, err = eng.Evaluate(ctx, events.CommunityMessage{
		TenantID: "sauvage", ChatID: -1001, ThreadID: 3, MessageID: 1, UserID: 10, HasPhoto: true,
	})
	require.NoError(t, err)
	_, err = incidentStore.Create(ctx, audit.Incident{
		Source: audit.SourceAutoMod, Status: audit.IncidentStatusOpen, Severity: audit.SeverityCritical,
		IdempotencyKey: "digest-llm", ReasonCode: audit.ReasonLLMOpenAI,
	})
	require.NoError(t, err)
	require.NoError(t, actionStore.Add(ctx, storage.ModerationActionEntry{
		EventID: "digest", IdempotencyKey: "digest-action", Command: "delete_message", Status: "completed",
	}))

	var sent tbapi.MessageConfig
	bot := &eventmocks.TbAPIMock{SendFunc: func(value tbapi.Chattable) (tbapi.Message, error) {
		var ok bool
		sent, ok = value.(tbapi.MessageConfig)
		require.True(t, ok)
		return tbapi.Message{}, nil
	}}
	digest, err := NewDigest(
		communityStore, actionStore, incidentStore, bot, -10099, 9, "Europe/Madrid",
		"https://sauvage-bot.e-dani.com",
	)
	require.NoError(t, err)
	require.NoError(t, digest.SendNow(ctx))
	require.Equal(t, int64(-10099), sent.ChatID)
	require.True(t, strings.Contains(sent.Text, "Incidentes Ornith / pendientes / críticos: 1 / 1 / 1"))
	require.True(t, strings.Contains(sent.Text, "Acciones Telegram correctas / fallidas: 1 / 0"))
	require.True(t, strings.Contains(sent.Text, "https://sauvage-bot.e-dani.com"))
}
