package events

import (
	"context"
	"strings"
	"testing"

	tbapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/redstone-md/shield/app/events/mocks"
)

type communityAssistantStoreStub struct {
	presentation CommunityPresentation
	members      map[string]CommunityMember
	reports      []CommunityUserReport
}

func (s *communityAssistantStoreStub) GetCommunityPresentation(
	_ context.Context, _, userID int64,
) (CommunityPresentation, bool, error) {
	return s.presentation, s.presentation.UserID == userID, nil
}

func (s *communityAssistantStoreStub) FindCommunityMemberByUsername(
	_ context.Context, _ int64, username string,
) (CommunityMember, bool, error) {
	member, found := s.members[strings.ToLower(strings.TrimPrefix(username, "@"))]
	return member, found, nil
}

func (s *communityAssistantStoreStub) GetCommunityMember(
	_ context.Context, _ int64, userID int64,
) (CommunityMember, bool, error) {
	for _, member := range s.members {
		if member.UserID == userID {
			return member, true, nil
		}
	}
	return CommunityMember{}, false, nil
}

func (s *communityAssistantStoreStub) CreateCommunityUserReport(
	_ context.Context, report CommunityUserReport,
) error {
	s.reports = append(s.reports, report)
	return nil
}

func TestCommunityPrivateAssistantPresentationAndReportFlow(t *testing.T) {
	var replies []string
	api := &mocks.TbAPIMock{SendFunc: func(c tbapi.Chattable) (tbapi.Message, error) {
		replies = append(replies, c.(tbapi.MessageConfig).Text)
		return tbapi.Message{}, nil
	}}
	store := &communityAssistantStoreStub{
		presentation: CommunityPresentation{
			ChatID: -1001234, ThreadID: 3, UserID: 7, FirstMessageID: 99,
		},
		members: map[string]CommunityMember{
			"target": {UserID: 42, UserName: "target", DisplayName: "Persona Objetivo"},
		},
	}
	assistant := newCommunityPrivateAssistant(api, store, -1001234, 3)
	sender := &tbapi.User{ID: 7, UserName: "reporter", FirstName: "Ana"}

	require.NoError(t, assistant.Handle(context.Background(), &tbapi.Message{
		Chat: tbapi.Chat{ID: 7, Type: "private"}, From: sender, Text: "/mipresentacion",
	}))
	require.Contains(t, replies[len(replies)-1], "https://t.me/c/1234/3/99")

	require.NoError(t, assistant.Handle(context.Background(), &tbapi.Message{
		Chat: tbapi.Chat{ID: 7, Type: "private"}, From: sender, Text: "/reportar @target",
	}))
	require.Contains(t, replies[len(replies)-1], "Persona Objetivo")
	require.NoError(t, assistant.Handle(context.Background(), &tbapi.Message{
		Chat: tbapi.Chat{ID: 7, Type: "private"}, From: sender,
		Text: "Ha compartido información privada sobre mí.",
	}))
	require.Len(t, store.reports, 1)
	assert.Equal(t, int64(42), store.reports[0].ReportedUserID)
	assert.Equal(t, "Ha compartido información privada sobre mí.", store.reports[0].Reason)
	assert.NotEmpty(t, store.reports[0].ReportKey)
	require.Contains(t, replies[len(replies)-1], "Reporte guardado")
}

func TestCommunityPrivateAssistantAcceptsForwardedUser(t *testing.T) {
	var replies []string
	api := &mocks.TbAPIMock{SendFunc: func(c tbapi.Chattable) (tbapi.Message, error) {
		replies = append(replies, c.(tbapi.MessageConfig).Text)
		return tbapi.Message{}, nil
	}}
	store := &communityAssistantStoreStub{}
	assistant := newCommunityPrivateAssistant(api, store, -1001234, 3)
	sender := &tbapi.User{ID: 7, FirstName: "Ana"}
	target := &tbapi.User{ID: 88, UserName: "forwarded", FirstName: "Luis"}

	require.NoError(t, assistant.Handle(context.Background(), &tbapi.Message{
		Chat: tbapi.Chat{ID: 7, Type: "private"}, From: sender,
		ForwardOrigin: &tbapi.MessageOrigin{Type: tbapi.MessageOriginUser, SenderUser: target},
	}))
	require.Contains(t, replies[len(replies)-1], "Luis @forwarded")
	require.NoError(t, assistant.Handle(context.Background(), &tbapi.Message{
		Chat: tbapi.Chat{ID: 7, Type: "private"}, From: sender, Text: "Motivo del reporte",
	}))
	require.Len(t, store.reports, 1)
	assert.Equal(t, int64(88), store.reports[0].ReportedUserID)
}

func TestConfigureCommunityPrivateCommandsRegistersExactlyThree(t *testing.T) {
	var configured tbapi.SetMyCommandsConfig
	listener := &TelegramListener{
		TbAPI: &mocks.TbAPIMock{RequestFunc: func(c tbapi.Chattable) (*tbapi.APIResponse, error) {
			configured = c.(tbapi.SetMyCommandsConfig)
			return &tbapi.APIResponse{Ok: true}, nil
		}},
		CommunityAssistantStore: &communityAssistantStoreStub{},
	}
	listener.configureCommunityPrivateCommands()
	require.Len(t, configured.Commands, 3)
	assert.Equal(t, []string{"normas", "mipresentacion", "reportar"}, []string{
		configured.Commands[0].Command, configured.Commands[1].Command, configured.Commands[2].Command,
	})
	require.NotNil(t, configured.Scope)
	assert.Equal(t, "all_private_chats", configured.Scope.Type)
}
