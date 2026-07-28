package events

import (
	"context"
	"fmt"
	"time"

	tbapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/hashicorp/go-multierror"

	"github.com/redstone-md/shield/app/bot"
	"github.com/redstone-md/shield/app/moderation"
	"github.com/redstone-md/shield/app/observability"
)

// CommunityMessage is the minimal Telegram context used by deterministic topic rules.
type CommunityMessage struct {
	TenantID        string
	ChatID          int64
	ThreadID        int
	MessageID       int
	MediaGroupID    string
	UserID          int64
	UserName        string
	Text            string
	HasPhoto        bool
	HasVideo        bool
	IsReply         bool
	IsBot           bool
	IsAdministrator bool
	ReceivedAt      time.Time
}

// CommunityDecision describes a deterministic topic-rule result.
type CommunityDecision struct {
	Handled     bool
	Enforce     bool
	Action      moderation.Action
	Rule        string
	Reason      string
	UserMessage string
	Duration    time.Duration
}

// CommunityModerator evaluates presentation and contest messages before any LLM.
type CommunityModerator interface {
	Evaluate(ctx context.Context, msg CommunityMessage) (CommunityDecision, error)
}

func (l *TelegramListener) handleCommunityMessage(ctx context.Context, update tbapi.Update) (bool, error) {
	if l.CommunityModerator == nil || update.Message == nil ||
		(update.Message.From == nil && update.Message.SenderChat == nil) {
		return false, nil
	}

	msg := update.Message
	var userID int64
	var userName string
	var isBot bool
	if msg.From != nil {
		userID = msg.From.ID
		userName = msg.From.UserName
		isBot = msg.From.IsBot
	}
	isAdministrator := l.SuperUsers.IsSuper(userName, userID)
	if msg.SenderChat != nil && (msg.SenderChat.ID == msg.Chat.ID || l.isLinkedChannel(msg)) {
		isAdministrator = true
	}

	decision, err := l.CommunityModerator.Evaluate(ctx, CommunityMessage{
		TenantID:        l.TenantID,
		ChatID:          msg.Chat.ID,
		ThreadID:        msg.MessageThreadID,
		MessageID:       msg.MessageID,
		MediaGroupID:    msg.MediaGroupID,
		UserID:          userID,
		UserName:        userName,
		Text:            messageText(msg),
		HasPhoto:        len(msg.Photo) > 0,
		HasVideo:        msg.Video != nil || msg.VideoNote != nil || msg.Story != nil || msg.Animation != nil,
		IsReply:         msg.ReplyToMessage != nil,
		IsBot:           isBot,
		IsAdministrator: isAdministrator,
		ReceivedAt:      msg.Time().UTC(),
	})
	if err != nil {
		return false, fmt.Errorf("evaluate community rules: %w", err)
	}
	if !decision.Handled {
		return false, nil
	}

	ctx = observability.WithModerationMetadata(
		ctx,
		fmt.Sprintf("community-%d-%d", msg.Chat.ID, msg.MessageID),
		fmt.Sprintf("community-%s-%d", l.TenantID, msg.Chat.ID),
		fmt.Sprintf("community:%s:chat:%d:message:%d", l.TenantID, msg.Chat.ID, msg.MessageID),
	)
	observability.Logf(ctx, "[INFO] community rule=%s action=%s enforce=%t chat=%d thread=%d message=%d user=%d reason=%s",
		decision.Rule, decision.Action, decision.Enforce, msg.Chat.ID, msg.MessageThreadID, msg.MessageID, userID,
		decision.Reason)
	if !decision.Enforce || l.Dry || l.TrainingMode || decision.Action == moderation.ActionAllow {
		// Topic rules and conduct moderation are complementary. An allowed or
		// shadow-mode topic decision must continue through the normal detector.
		return false, nil
	}

	actions := l.ActionExecutor
	if actions == nil {
		executor := newTelegramActionExecutor(l.TbAPI, l.Dry, l.TrainingMode, l.SuperUsers, l.ModerationActions)
		actions = executor
	}

	var result *multierror.Error
	if err = actions.DeleteMessage(ctx, msg.Chat.ID, msg.MessageID); err != nil {
		result = multierror.Append(result, fmt.Errorf("delete community-rule message: %w", err))
	}
	if decision.UserMessage != "" {
		err = actions.WarnUser(ctx, warnRequest{
			chatID:      msg.Chat.ID,
			threadID:    msg.MessageThreadID,
			subjectID:   userID,
			messageID:   msg.MessageID,
			text:        decision.UserMessage,
			warnDelTime: l.ModerationConfig.WarnDeleteDuration,
		})
		if err != nil {
			result = multierror.Append(result, fmt.Errorf("send community-rule warning: %w", err))
		}
	}

	switch decision.Action {
	case moderation.ActionRestrict:
		err = actions.ApplyBan(ctx, banRequest{
			duration: decision.Duration,
			userID:   userID,
			userName: userName,
			chatID:   msg.Chat.ID,
			dry:      l.Dry,
			training: l.TrainingMode,
			restrict: true,
		})
	case moderation.ActionBan:
		err = actions.ApplyBan(ctx, banRequest{
			duration: bot.PermanentBanDuration,
			userID:   userID,
			userName: userName,
			chatID:   msg.Chat.ID,
			dry:      l.Dry,
			training: l.TrainingMode,
		})
	}
	if err != nil {
		result = multierror.Append(result, fmt.Errorf("apply community-rule action: %w", err))
	}
	return true, result.ErrorOrNil()
}

func messageText(msg *tbapi.Message) string {
	if msg.Text != "" {
		return msg.Text
	}
	return msg.Caption
}
