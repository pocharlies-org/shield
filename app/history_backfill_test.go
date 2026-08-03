package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/redstone-md/shield/app/events"
)

func TestHistoricalMessageFromArchiveRowTreatsTopicRootAsTopLevelPost(t *testing.T) {
	message, err := historicalMessageFromArchiveRow(archiveMessageRow{
		ArchiveID:       "tg_-1003672565710_51183",
		SenderID:        "tg_961795896",
		SenderName:      "Enric",
		ReceivedAt:      time.Date(2026, 7, 22, 16, 24, 20, 0, time.UTC),
		Content:         "Acepto privados",
		MessageType:     "PHOTO",
		ReplyToMessage:  "tg_-1003672565710_3",
		TopicID:         3,
		TelegramMessage: 51183,
	}, -1003672565710, "personal")
	require.NoError(t, err)
	assert.False(t, message.IsReply)
	assert.True(t, message.HasPhoto)
	assert.Equal(t, int64(961795896), message.UserID)
	assert.Equal(t, "telegram:archive:personal:tg_-1003672565710_51183", message.IdempotencyKey)
}

func TestHistoricalMessageFromArchiveRowKeepsRealReply(t *testing.T) {
	message, err := historicalMessageFromArchiveRow(archiveMessageRow{
		ArchiveID:      "tg_-1001_20",
		SenderID:       "tg_42",
		ReceivedAt:     time.Now(),
		MessageType:    "TEXT",
		ReplyToMessage: "tg_-1001_19",
		TopicID:        3,
	}, -1001, "personal")
	require.NoError(t, err)
	assert.True(t, message.IsReply)
}

func TestArchivedTopicFromReplyRecoversLegacyPresentationTopic(t *testing.T) {
	topics := map[int]struct{}{3: {}, 6: {}}

	assert.Equal(t, 3, archivedTopicFromReply("tg_-1003672565710_3", topics))
	assert.Equal(t, 6, archivedTopicFromReply("6", topics))
	assert.Zero(t, archivedTopicFromReply("tg_-1003672565710_568", topics))
	assert.Zero(t, archivedTopicFromReply("not-a-message", topics))
}

func TestHistoricalMessageFromLegacyArchiveRowTreatsRecoveredRootAsTopLevelPost(t *testing.T) {
	row := archiveMessageRow{
		ArchiveID:      "tg_-1003672565710_568",
		SenderID:       "tg_6524317158",
		SenderName:     "LadyVibraphone",
		ReceivedAt:     time.Date(2026, 4, 6, 9, 12, 11, 0, time.UTC),
		Content:        "Hola, soy una mujer de 45 años.",
		MessageType:    "PHOTO",
		ReplyToMessage: "tg_-1003672565710_3",
	}
	row.TopicID = archivedTopicFromReply(row.ReplyToMessage, map[int]struct{}{3: {}})

	message, err := historicalMessageFromArchiveRow(row, -1003672565710, "personal")
	require.NoError(t, err)
	assert.Equal(t, 3, message.ThreadID)
	assert.False(t, message.IsReply)
	assert.Equal(t, 568, message.MessageID)
	assert.Equal(t, int64(6524317158), message.UserID)
}

func TestAssignSyntheticMediaGroupsReconstructsAlbumsAndHonorsWindow(t *testing.T) {
	base := time.Date(2026, 7, 22, 16, 24, 20, 0, time.UTC)
	messages := []events.HistoricalMessage{
		{ThreadID: 3, UserID: 42, MessageID: 1, HasPhoto: true, ReceivedAt: base},
		{ThreadID: 3, UserID: 42, MessageID: 2, HasPhoto: true, ReceivedAt: base.Add(time.Second)},
		{ThreadID: 3, UserID: 7, MessageID: 3, HasPhoto: true, ReceivedAt: base.Add(time.Second)},
		{ThreadID: 3, UserID: 42, MessageID: 4, HasPhoto: true, ReceivedAt: base.Add(4 * time.Second)},
	}

	assignSyntheticMediaGroups(messages, 2*time.Second)

	assert.Equal(t, "archive:3:42:1", messages[0].MediaGroupID)
	assert.Equal(t, messages[0].MediaGroupID, messages[1].MediaGroupID)
	assert.Empty(t, messages[2].MediaGroupID)
	assert.Empty(t, messages[3].MediaGroupID)
}

func TestAssignSyntheticMediaGroupsSplitsAtTelegramAlbumLimit(t *testing.T) {
	base := time.Now()
	messages := make([]events.HistoricalMessage, 11)
	for idx := range messages {
		messages[idx] = events.HistoricalMessage{
			ThreadID: 3, UserID: 42, MessageID: idx + 1, HasPhoto: true, ReceivedAt: base,
		}
	}
	assignSyntheticMediaGroups(messages, 2*time.Second)

	assert.Equal(t, "archive:3:42:1", messages[0].MediaGroupID)
	assert.Equal(t, messages[0].MediaGroupID, messages[9].MediaGroupID)
	assert.Empty(t, messages[10].MediaGroupID)
}

func TestParseTelegramIdentifiers(t *testing.T) {
	userID, err := parseTelegramScopedID("tg_961795896")
	require.NoError(t, err)
	assert.Equal(t, int64(961795896), userID)

	messageID, err := parseTelegramMessageID("tg_-1003672565710_51183")
	require.NoError(t, err)
	assert.Equal(t, 51183, messageID)
}
