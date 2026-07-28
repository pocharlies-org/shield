package events

import (
	"testing"

	tbapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthorizeAdminCallbackChecksChatAndActor(t *testing.T) {
	supers := SuperUsers{"123"}

	allowed, err := authorizeAdminCallback(&tbapi.CallbackQuery{
		From:    &tbapi.User{ID: 123},
		Message: &tbapi.Message{Chat: tbapi.Chat{ID: 456}},
	}, 456, supers)
	require.NoError(t, err)
	assert.True(t, allowed)

	allowed, err = authorizeAdminCallback(&tbapi.CallbackQuery{
		From:    &tbapi.User{ID: 999},
		Message: &tbapi.Message{Chat: tbapi.Chat{ID: 456}},
	}, 456, supers)
	require.Error(t, err)
	assert.False(t, allowed)

	allowed, err = authorizeAdminCallback(&tbapi.CallbackQuery{
		From:    &tbapi.User{ID: 123},
		Message: &tbapi.Message{Chat: tbapi.Chat{ID: 789}},
	}, 456, supers)
	require.NoError(t, err)
	assert.False(t, allowed)

	allowed, err = authorizeAdminCallback(nil, 456, supers)
	require.Error(t, err)
	assert.False(t, allowed)
}
