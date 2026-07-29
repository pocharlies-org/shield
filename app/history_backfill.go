package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	tbapi "github.com/OvyFlash/telegram-bot-api"

	"github.com/redstone-md/shield/app/events"
	"github.com/redstone-md/shield/app/storage/engine"
)

const maxTelegramAlbumSize = 10

type archiveMessageRow struct {
	ArchiveID       string    `db:"archive_id"`
	SenderID        string    `db:"sender_id"`
	SenderName      string    `db:"sender_name"`
	ReceivedAt      time.Time `db:"received_at"`
	Content         string    `db:"content"`
	MessageType     string    `db:"message_type"`
	ReplyToMessage  string    `db:"reply_to_message"`
	TopicID         int       `db:"topic_id"`
	TelegramMessage int       `db:"telegram_message_id"`
}

type historyBackfillLoadSummary struct {
	SourceRows      int
	SelectedRows    int
	SkippedInvalid  int
	SkippedExisting int
}

func runCommunityHistoryBackfill(ctx context.Context, opts options, assembly *runtimeAssembly) error {
	if assembly == nil || assembly.DataDB == nil {
		return fmt.Errorf("historical backfill requires an initialized target database")
	}
	if !opts.Dry || !assembly.ActiveRuleSet.Moderation.DryRun {
		return fmt.Errorf("historical backfill requires DRY=true")
	}
	if opts.Community.ApplyActions {
		return fmt.Errorf("historical backfill requires COMMUNITY_APPLY_ACTIONS=false")
	}
	if !opts.Community.Enabled || assembly.CommunityModerator == nil {
		return fmt.Errorf("historical backfill requires community moderation")
	}
	if opts.Backfill.Lookback <= 0 {
		return fmt.Errorf("historical backfill lookback must be positive")
	}
	if opts.Backfill.AlbumWindow < 0 {
		return fmt.Errorf("historical backfill album window cannot be negative")
	}

	conversationID := strings.TrimSpace(opts.Backfill.ConversationID)
	if conversationID == "" {
		conversationID = fmt.Sprintf("tg_%d", opts.Community.ChatID)
	}
	sourceDB, err := engine.New(ctx, opts.Backfill.SourceDB, "sauvage-history-source")
	if err != nil {
		return fmt.Errorf("open historical source database: %w", err)
	}
	defer sourceDB.Close()
	if sourceDB.Type() != engine.Postgres {
		return fmt.Errorf("historical source database must be PostgreSQL")
	}

	messages, loadSummary, err := loadArchivedTelegramMessages(ctx, sourceDB, opts, conversationID)
	if err != nil {
		return err
	}
	existing, err := existingTargetMessageIDs(ctx, assembly.DataDB, opts.InstanceID, opts.Community.ChatID)
	if err != nil {
		return err
	}
	selected := messages[:0]
	for _, message := range messages {
		if _, found := existing[message.MessageID]; found {
			loadSummary.SkippedExisting++
			continue
		}
		selected = append(selected, message)
	}

	fakeTelegram := &tbapi.BotAPI{}
	listener := assembly.makeTelegramListener(opts, fakeTelegram)
	analysis, err := listener.AnalyzeHistoricalMessages(ctx, selected)
	if err != nil {
		return err
	}
	log.Printf(
		"[INFO] historical backfill completed: source=%d selected=%d existing=%d invalid=%d processed=%d topics=%v lookback=%s",
		loadSummary.SourceRows,
		loadSummary.SelectedRows,
		loadSummary.SkippedExisting,
		loadSummary.SkippedInvalid,
		analysis.Processed,
		historyBackfillTopics(opts),
		opts.Backfill.Lookback,
	)
	return nil
}

func loadArchivedTelegramMessages(
	ctx context.Context, sourceDB *engine.SQL, opts options, conversationID string,
) ([]events.HistoricalMessage, historyBackfillLoadSummary, error) {
	var rows []archiveMessageRow
	query := `
		SELECT
			wa_message_id AS archive_id,
			COALESCE(sender_wa_id, '') AS sender_id,
			COALESCE(metadata->>'sender_name', '') AS sender_name,
			wa_timestamp AS received_at,
			COALESCE(content, '') AS content,
			UPPER(COALESCE(message_type, 'TEXT')) AS message_type,
			COALESCE(reply_to_message_id, '') AS reply_to_message,
			COALESCE((NULLIF(metadata->>'topic_id', ''))::INTEGER, 0) AS topic_id,
			COALESCE((NULLIF(metadata->>'telegram_message_id', ''))::INTEGER, 0) AS telegram_message_id
		FROM messages
		WHERE platform = 'telegram'
		  AND account = $1
		  AND conversation_id = $2
		  AND direction = 'INBOUND'
		  AND wa_timestamp >= CURRENT_TIMESTAMP - ($3 * INTERVAL '1 second')
		  AND wa_timestamp <= CURRENT_TIMESTAMP
		ORDER BY wa_timestamp ASC, id ASC`
	if err := sourceDB.SelectContext(
		ctx,
		&rows,
		query,
		strings.TrimSpace(opts.Backfill.Account),
		conversationID,
		int64(opts.Backfill.Lookback/time.Second),
	); err != nil {
		return nil, historyBackfillLoadSummary{}, fmt.Errorf("load archived Telegram messages: %w", err)
	}

	topics := make(map[int]struct{})
	for _, topicID := range historyBackfillTopics(opts) {
		topics[topicID] = struct{}{}
	}
	summary := historyBackfillLoadSummary{SourceRows: len(rows)}
	messages := make([]events.HistoricalMessage, 0, len(rows))
	for _, row := range rows {
		if _, selected := topics[row.TopicID]; !selected {
			continue
		}
		message, err := historicalMessageFromArchiveRow(row, opts.Community.ChatID, opts.Backfill.Account)
		if err != nil {
			summary.SkippedInvalid++
			log.Printf("[WARN] skip invalid archived Telegram message %q: %v", row.ArchiveID, err)
			continue
		}
		messages = append(messages, message)
	}
	assignSyntheticMediaGroups(messages, opts.Backfill.AlbumWindow)
	summary.SelectedRows = len(messages)
	return messages, summary, nil
}

func historyBackfillTopics(opts options) []int {
	if len(opts.Backfill.Topics) > 0 {
		return opts.Backfill.Topics
	}
	return []int{opts.Community.PresentationThreadID, opts.Community.ContestThreadID}
}

func historicalMessageFromArchiveRow(
	row archiveMessageRow, chatID int64, account string,
) (events.HistoricalMessage, error) {
	userID, err := parseTelegramScopedID(row.SenderID)
	if err != nil {
		return events.HistoricalMessage{}, fmt.Errorf("parse sender id: %w", err)
	}
	messageID := row.TelegramMessage
	if messageID <= 0 {
		messageID, err = parseTelegramMessageID(row.ArchiveID)
		if err != nil {
			return events.HistoricalMessage{}, fmt.Errorf("parse message id: %w", err)
		}
	}

	isReply := false
	if strings.TrimSpace(row.ReplyToMessage) != "" {
		replyID, replyErr := parseTelegramMessageID(row.ReplyToMessage)
		if replyErr != nil {
			return events.HistoricalMessage{}, fmt.Errorf("parse reply id: %w", replyErr)
		}
		// Telegram archives the forum topic root as reply_to_message_id for
		// ordinary top-level topic posts. It is not a member reply.
		isReply = replyID != row.TopicID
	}

	messageType := strings.ToUpper(strings.TrimSpace(row.MessageType))
	return events.HistoricalMessage{
		IdempotencyKey: fmt.Sprintf(
			"telegram:archive:%s:%s",
			strings.TrimSpace(account),
			strings.TrimSpace(row.ArchiveID),
		),
		ChatID:          chatID,
		ThreadID:        row.TopicID,
		MessageID:       messageID,
		UserID:          userID,
		UserName:        strings.TrimSpace(row.SenderName),
		Text:            row.Content,
		HasPhoto:        messageType == "PHOTO",
		HasVideo:        messageType == "VIDEO" || messageType == "VIDEO_NOTE" || messageType == "ANIMATION",
		IsReply:         isReply,
		IsAdministrator: userID == chatID || userID < 0,
		ReceivedAt:      row.ReceivedAt.UTC(),
	}, nil
}

func parseTelegramScopedID(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "tg_")
	id, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("invalid Telegram id %q", value)
	}
	return id, nil
}

func parseTelegramMessageID(value string) (int, error) {
	trimmed := strings.TrimSpace(value)
	idx := strings.LastIndex(trimmed, "_")
	if idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	id, err := strconv.Atoi(trimmed)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid Telegram message id %q", value)
	}
	return id, nil
}

func assignSyntheticMediaGroups(messages []events.HistoricalMessage, window time.Duration) {
	type album struct {
		indexes []int
		last    time.Time
	}
	active := make(map[string]*album)
	finish := func(item *album) {
		if item == nil || len(item.indexes) < 2 {
			return
		}
		first := messages[item.indexes[0]]
		groupID := fmt.Sprintf("archive:%d:%d:%d", first.ThreadID, first.UserID, first.MessageID)
		for _, idx := range item.indexes {
			messages[idx].MediaGroupID = groupID
		}
	}

	for idx := range messages {
		message := messages[idx]
		if !message.HasPhoto {
			continue
		}
		key := fmt.Sprintf("%d:%d", message.ThreadID, message.UserID)
		current := active[key]
		gap := time.Duration(0)
		if current != nil {
			gap = message.ReceivedAt.Sub(current.last)
		}
		if current == nil || gap < 0 || gap > window || len(current.indexes) >= maxTelegramAlbumSize {
			finish(current)
			current = &album{}
			active[key] = current
		}
		current.indexes = append(current.indexes, idx)
		current.last = message.ReceivedAt
	}
	for _, item := range active {
		finish(item)
	}
}

func existingTargetMessageIDs(
	ctx context.Context, targetDB *engine.SQL, tenantID string, chatID int64,
) (map[int]struct{}, error) {
	var ids []int
	query := targetDB.Adopt(`SELECT DISTINCT message_id
		FROM incoming_events
		WHERE tenant_id = ? AND chat_id = ? AND message_id > 0`)
	if err := targetDB.SelectContext(ctx, &ids, query, tenantID, chatID); err != nil {
		return nil, fmt.Errorf("load existing target message ids: %w", err)
	}
	result := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		result[id] = struct{}{}
	}
	return result, nil
}
