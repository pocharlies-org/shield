package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	tbapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/redstone-md/shield/app/community"
	"github.com/redstone-md/shield/app/events"
	"github.com/redstone-md/shield/app/storage/engine"
)

type communityMemberLookupFunc func(tbapi.GetChatMemberConfig) (tbapi.ChatMember, error)

func (fn communityMemberLookupFunc) GetChatMember(
	config tbapi.GetChatMemberConfig,
) (tbapi.ChatMember, error) {
	return fn(config)
}

func TestCommunityIdentityRefresherLoadsCurrentTelegramUsername(t *testing.T) {
	ctx := context.Background()
	db, err := engine.NewSqlite(filepath.Join(t.TempDir(), "community.db"), "sauvage")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	store, err := community.NewStore(ctx, db)
	require.NoError(t, err)
	observedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, store.ObserveMember(ctx, events.CommunityMember{
		ChatID: -1001, UserID: 42, DisplayName: "Ana",
		LastMessageID: 99, LastThreadID: 3, LastMessageAt: observedAt,
	}))

	var requested tbapi.GetChatMemberConfig
	refresher := newCommunityIdentityRefresher(store, communityMemberLookupFunc(
		func(config tbapi.GetChatMemberConfig) (tbapi.ChatMember, error) {
			requested = config
			return tbapi.ChatMember{Status: "member", User: &tbapi.User{
				ID: 42, UserName: "ana_actual", FirstName: "Ana", LastName: "Real",
			}}, nil
		},
	), -1001)
	refresher.requestInterval = 0

	updated, failed, err := refresher.Refresh(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, updated)
	assert.Zero(t, failed)
	assert.Equal(t, int64(-1001), requested.ChatID)
	assert.Equal(t, int64(42), requested.UserID)

	member, found, err := store.GetCommunityMember(ctx, -1001, 42)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "ana_actual", member.UserName)
	assert.Equal(t, "Ana Real", member.DisplayName)
	assert.Equal(t, 99, member.LastMessageID)
	assert.True(t, observedAt.Equal(member.LastMessageAt))
}
