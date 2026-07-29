package events

import (
	"context"
	"fmt"
	"strings"
	"time"

	tbapi "github.com/OvyFlash/telegram-bot-api"

	"github.com/redstone-md/shield/app/moderation"
)

// HistoricalMessage is an archived Telegram message that can be evaluated by
// the normal Shield pipeline without contacting Telegram.
type HistoricalMessage struct {
	IdempotencyKey  string
	ChatID          int64
	ThreadID        int
	MessageID       int
	MediaGroupID    string
	UserID          int64
	UserName        string
	DisplayName     string
	Text            string
	HasPhoto        bool
	HasVideo        bool
	IsReply         bool
	IsBot           bool
	IsAdministrator bool
	ReceivedAt      time.Time
}

// HistoricalAnalysisSummary reports a completed one-shot historical analysis.
type HistoricalAnalysisSummary struct {
	Processed int
}

// AnalyzeHistoricalMessages evaluates archived messages in the supplied order.
// It is deliberately unavailable outside dry/training mode and never invokes
// the Telegram-facing community action path.
func (l *TelegramListener) AnalyzeHistoricalMessages(
	ctx context.Context, messages []HistoricalMessage,
) (HistoricalAnalysisSummary, error) {
	if !l.Dry && !l.TrainingMode {
		return HistoricalAnalysisSummary{}, fmt.Errorf("historical analysis requires dry or training mode")
	}

	summary := HistoricalAnalysisSummary{}
	defer l.shutdownPipeline()

	for idx, archived := range messages {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		if err := validateHistoricalMessage(archived); err != nil {
			return summary, fmt.Errorf("historical message %d: %w", idx, err)
		}
		if err := l.analyzeHistoricalMessage(ctx, archived); err != nil {
			return summary, fmt.Errorf("analyze historical message %d (%d): %w", idx, archived.MessageID, err)
		}
		summary.Processed++
	}
	return summary, nil
}

func validateHistoricalMessage(msg HistoricalMessage) error {
	if strings.TrimSpace(msg.IdempotencyKey) == "" {
		return fmt.Errorf("idempotency key is required")
	}
	if msg.ChatID == 0 {
		return fmt.Errorf("chat id is required")
	}
	if msg.MessageID <= 0 {
		return fmt.Errorf("message id must be positive")
	}
	if msg.UserID == 0 {
		return fmt.Errorf("user id is required")
	}
	if msg.ReceivedAt.IsZero() {
		return fmt.Errorf("received timestamp is required")
	}
	return nil
}

func (l *TelegramListener) analyzeHistoricalMessage(ctx context.Context, archived HistoricalMessage) error {
	if l.CommunityModerator != nil {
		_, err := l.CommunityModerator.Evaluate(ctx, CommunityMessage{
			TenantID:     l.TenantID,
			ChatID:       archived.ChatID,
			ThreadID:     archived.ThreadID,
			MessageID:    archived.MessageID,
			MediaGroupID: archived.MediaGroupID,
			UserID:       archived.UserID,
			UserName:     archived.UserName,
			DisplayName:  archived.DisplayName,
			Text:         archived.Text,
			HasPhoto:     archived.HasPhoto,
			HasVideo:     archived.HasVideo,
			IsReply:      archived.IsReply,
			IsBot:        archived.IsBot,
			IsAdministrator: archived.IsAdministrator ||
				l.SuperUsers.IsSuper(archived.UserName, archived.UserID),
			ReceivedAt: archived.ReceivedAt.UTC(),
		})
		if err != nil {
			return fmt.Errorf("evaluate historical community rules: %w", err)
		}
	}

	// Archived media is intentionally represented as metadata only. This lets
	// Ornith review a stored caption without trying to download a stale Telegram
	// file identifier.
	update := tbapi.Update{Message: &tbapi.Message{
		MessageID:       archived.MessageID,
		MessageThreadID: archived.ThreadID,
		MediaGroupID:    archived.MediaGroupID,
		Date:            int(archived.ReceivedAt.Unix()),
		Text:            archived.Text,
		IsTopicMessage:  archived.ThreadID > 0,
		Chat: tbapi.Chat{
			ID:   archived.ChatID,
			Type: "supergroup",
		},
		From: &tbapi.User{
			ID:        archived.UserID,
			UserName:  archived.UserName,
			FirstName: archived.DisplayName,
			IsBot:     archived.IsBot,
		},
	}}

	msg := transform(update.Message)
	event := l.makeIncomingEvent(update, msg)
	event.Source = "telegram.archive"
	event.EventID = fmt.Sprintf("backfill-%s-%d", strings.TrimSpace(l.TenantID), archived.MessageID)
	event.CorrelationID = fmt.Sprintf("corr-%s-backfill", strings.TrimSpace(l.TenantID))
	event.IdempotencyKey = archived.IdempotencyKey
	event.MediaGroupID = archived.MediaGroupID
	event.Content.HasMedia = archived.HasPhoto || archived.HasVideo
	event.Content.Attributes["historical"] = "true"
	event.Subject = moderation.Subject{
		ID:       archived.UserID,
		UserName: archived.UserName,
		IsBot:    archived.IsBot,
	}
	event.ReceivedAt = archived.ReceivedAt.UTC()

	return l.enqueueIncomingEvent(ctx, event, update)
}
