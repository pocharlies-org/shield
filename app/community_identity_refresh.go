package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	tbapi "github.com/OvyFlash/telegram-bot-api"

	"github.com/redstone-md/shield/app/community"
)

const (
	communityIdentityRefreshInterval = 24 * time.Hour
	communityIdentityRequestInterval = 50 * time.Millisecond
)

type communityMemberLookup interface {
	GetChatMember(config tbapi.GetChatMemberConfig) (tbapi.ChatMember, error)
}

type communityIdentityRefresher struct {
	store           *community.Store
	api             communityMemberLookup
	chatID          int64
	refreshInterval time.Duration
	requestInterval time.Duration
}

func newCommunityIdentityRefresher(
	store *community.Store, api communityMemberLookup, chatID int64,
) *communityIdentityRefresher {
	return &communityIdentityRefresher{
		store: store, api: api, chatID: chatID,
		refreshInterval: communityIdentityRefreshInterval,
		requestInterval: communityIdentityRequestInterval,
	}
}

func (r *communityIdentityRefresher) Run(ctx context.Context) {
	r.refreshAndLog(ctx)
	ticker := time.NewTicker(r.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.refreshAndLog(ctx)
		}
	}
}

func (r *communityIdentityRefresher) refreshAndLog(ctx context.Context) {
	updated, failed, err := r.Refresh(ctx)
	if err != nil {
		log.Printf("[WARN] community identity refresh failed: %v", err)
		return
	}
	log.Printf("[INFO] community identity refresh completed: updated=%d failed=%d", updated, failed)
}

func (r *communityIdentityRefresher) Refresh(ctx context.Context) (updated, failed int, err error) {
	if r.store == nil || r.api == nil || r.chatID == 0 {
		return 0, 0, fmt.Errorf("community identity refresh is not configured")
	}
	members, err := r.store.ListMembers(ctx, community.MemberFilter{ChatID: r.chatID, Limit: 1000})
	if err != nil {
		return 0, 0, fmt.Errorf("list community identity candidates: %w", err)
	}
	for idx, member := range members {
		if idx > 0 && r.requestInterval > 0 {
			timer := time.NewTimer(r.requestInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return updated, failed, ctx.Err()
			case <-timer.C:
			}
		}
		chatMember, lookupErr := r.api.GetChatMember(tbapi.GetChatMemberConfig{
			ChatConfigWithUser: tbapi.ChatConfigWithUser{
				ChatConfig: tbapi.ChatConfig{ChatID: r.chatID},
				UserID:     member.UserID,
			},
		})
		if lookupErr != nil || chatMember.User == nil {
			failed++
			continue
		}
		displayName := strings.TrimSpace(chatMember.User.FirstName + " " + chatMember.User.LastName)
		if updateErr := r.store.UpdateMemberIdentity(
			ctx, r.chatID, member.UserID, chatMember.User.UserName, displayName,
		); updateErr != nil {
			failed++
			continue
		}
		updated++
	}
	return updated, failed, nil
}
