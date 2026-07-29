package events

import (
	"context"
	"testing"
	"time"

	tbapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/redstone-md/shield/app/events/mocks"
)

func TestTelegramListenerExplicitlyRequestsConsumedUpdates(t *testing.T) {
	var received tbapi.UpdateConfig
	updates := make(chan tbapi.Update)
	close(updates)
	mockAPI := &mocks.TbAPIMock{
		GetUpdatesChanFunc: func(config tbapi.UpdateConfig) tbapi.UpdatesChannel {
			received = config
			return tbapi.UpdatesChannel(updates)
		},
	}
	listener := &TelegramListener{TbAPI: mockAPI, IdleDuration: time.Hour}

	err := listener.eventLoop(context.Background())
	require.Error(t, err)
	assert.Equal(t, []string{
		tbapi.UpdateTypeMessage,
		tbapi.UpdateTypeEditedMessage,
		tbapi.UpdateTypeCallbackQuery,
	}, received.AllowedUpdates)
}
