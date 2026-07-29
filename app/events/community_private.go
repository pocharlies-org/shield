package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	tbapi "github.com/OvyFlash/telegram-bot-api"
)

const sauvageRulesText = `Normas básicas de Sauvage:

• Trata a las demás personas con respeto. No se permiten insultos, acoso, humillaciones ni ataques.
• No difundas ni comentes la vida privada de otra persona.
• Es una comunidad de adultos: el consentimiento, los límites y la privacidad son obligatorios.
• En Presentaciones solo se publica una presentación real por persona, con foto y texto. No se conversa ni se responde en ese topic.
• En cada concurso solo se admite una participación de fotos por persona.
• Si tienes un problema con alguien, usa /reportar en privado. El reporte queda para revisión del equipo y no aplica sanciones automáticamente.`

type privateReportStage int

const (
	reportAwaitingTarget privateReportStage = iota + 1
	reportAwaitingReason
)

type privateReportState struct {
	stage           privateReportStage
	target          CommunityMember
	sourceMessageID int
	sourceThreadID  int
}

type communityPrivateAssistant struct {
	api                  TbAPI
	store                CommunityAssistantStore
	chatID               int64
	presentationThreadID int
	mu                   sync.Mutex
	states               map[int64]privateReportState
}

func newCommunityPrivateAssistant(
	api TbAPI, store CommunityAssistantStore, chatID int64, presentationThreadID int,
) *communityPrivateAssistant {
	return &communityPrivateAssistant{
		api: api, store: store, chatID: chatID, presentationThreadID: presentationThreadID,
		states: make(map[int64]privateReportState),
	}
}

func (l *TelegramListener) configureCommunityPrivateCommands() {
	if l.CommunityAssistantStore == nil || l.TbAPI == nil {
		return
	}
	config := tbapi.NewSetMyCommandsWithScope(
		tbapi.NewBotCommandScopeAllPrivateChats(),
		tbapi.BotCommand{Command: "normas", Description: "Consultar las normas de Sauvage"},
		tbapi.BotCommand{Command: "mipresentacion", Description: "Comprobar y abrir mi presentación"},
		tbapi.BotCommand{Command: "reportar", Description: "Reportar a una persona al equipo"},
	)
	if _, err := l.TbAPI.Request(config); err != nil {
		log.Printf("[WARN] failed to configure Sauvage private commands: %v", err)
	}
}

func (a *communityPrivateAssistant) Handle(ctx context.Context, msg *tbapi.Message) error {
	if msg == nil || msg.From == nil {
		return nil
	}
	text := strings.TrimSpace(messageText(msg))
	command, argument := privateCommand(text)

	switch command {
	case "start":
		a.clearState(msg.From.ID)
		return a.reply(msg.Chat.ID, "Puedo ayudarte con tres cosas:\n\n/normas — ver las normas\n/mipresentacion — comprobar tu presentación\n/reportar — reportar a una persona")
	case "normas":
		a.clearState(msg.From.ID)
		return a.reply(msg.Chat.ID, sauvageRulesText)
	case "mipresentacion":
		a.clearState(msg.From.ID)
		return a.sendPresentation(ctx, msg)
	case "reportar", "report":
		if argument == "" {
			a.setState(msg.From.ID, privateReportState{stage: reportAwaitingTarget})
			return a.reply(msg.Chat.ID, "Envía ahora el @usuario de la persona o reenvíame uno de sus mensajes.")
		}
		return a.beginReportByUsername(ctx, msg, argument)
	case "cancelar", "cancel":
		a.clearState(msg.From.ID)
		return a.reply(msg.Chat.ID, "Operación cancelada.")
	}

	state, hasState := a.getState(msg.From.ID)
	if target, ok := forwardedCommunityMember(msg); ok {
		return a.beginReportForTarget(msg, target, 0, 0)
	}
	if msg.ForwardOrigin != nil {
		return a.reply(msg.Chat.ID, "Telegram ha ocultado la identidad de ese mensaje. Envíame el @usuario de la persona.")
	}
	if !hasState {
		return a.reply(msg.Chat.ID, "Usa /normas, /mipresentacion o /reportar.")
	}
	switch state.stage {
	case reportAwaitingTarget:
		return a.beginReportByUsername(ctx, msg, text)
	case reportAwaitingReason:
		return a.finishReport(ctx, msg, state, text)
	default:
		a.clearState(msg.From.ID)
		return a.reply(msg.Chat.ID, "Usa /reportar para empezar de nuevo.")
	}
}

func (a *communityPrivateAssistant) sendPresentation(ctx context.Context, msg *tbapi.Message) error {
	presentation, found, err := a.store.GetCommunityPresentation(ctx, a.chatID, msg.From.ID)
	if err != nil {
		return fmt.Errorf("find member presentation: %w", err)
	}
	if !found {
		return a.reply(msg.Chat.ID, "No encuentro una presentación registrada a tu nombre.")
	}
	threadID := presentation.ThreadID
	if threadID == 0 {
		threadID = a.presentationThreadID
	}
	link := telegramForumMessageURL(presentation.ChatID, threadID, presentation.FirstMessageID)
	return a.reply(msg.Chat.ID, "Sí, tienes una presentación registrada:\n"+link)
}

func (a *communityPrivateAssistant) beginReportByUsername(
	ctx context.Context, msg *tbapi.Message, value string,
) error {
	username := strings.TrimPrefix(strings.TrimSpace(value), "@")
	if fields := strings.Fields(username); len(fields) > 0 {
		username = fields[0]
	}
	if username == "" {
		return a.reply(msg.Chat.ID, "Necesito un @usuario o un mensaje reenviado de esa persona.")
	}
	target, found, err := a.store.FindCommunityMemberByUsername(ctx, a.chatID, username)
	if err != nil {
		return fmt.Errorf("resolve reported username: %w", err)
	}
	if !found {
		return a.reply(msg.Chat.ID, "No encuentro a @"+username+" entre los usuarios observados. Reenvíame un mensaje suyo para identificarlo.")
	}
	return a.beginReportForTarget(msg, target, 0, 0)
}

func (a *communityPrivateAssistant) beginReportForTarget(
	msg *tbapi.Message, target CommunityMember, sourceMessageID, sourceThreadID int,
) error {
	if target.UserID == 0 {
		return a.reply(msg.Chat.ID, "Telegram ha ocultado la identidad del remitente. Envíame su @usuario.")
	}
	if target.UserID == msg.From.ID {
		return a.reply(msg.Chat.ID, "No puedes reportarte a ti mismo.")
	}
	a.setState(msg.From.ID, privateReportState{
		stage: reportAwaitingReason, target: target,
		sourceMessageID: sourceMessageID, sourceThreadID: sourceThreadID,
	})
	return a.reply(msg.Chat.ID, "Vas a reportar a "+communityMemberLabel(target)+". Escribe ahora el motivo con el detalle necesario.")
}

func (a *communityPrivateAssistant) finishReport(
	ctx context.Context, msg *tbapi.Message, state privateReportState, reason string,
) error {
	if reason == "" || strings.HasPrefix(reason, "/") {
		return a.reply(msg.Chat.ID, "Escribe el motivo del reporte en un mensaje de texto, o usa /cancelar.")
	}
	runes := []rune(reason)
	if len(runes) > 1500 {
		return a.reply(msg.Chat.ID, "El motivo es demasiado largo. Resúmelo en un máximo de 1500 caracteres.")
	}
	key, err := newCommunityReportKey()
	if err != nil {
		return fmt.Errorf("create report key: %w", err)
	}
	reporterName := strings.TrimSpace(strings.Join([]string{msg.From.FirstName, msg.From.LastName}, " "))
	report := CommunityUserReport{
		ReportKey: key, ChatID: a.chatID,
		ReporterUserID: msg.From.ID, ReporterUserName: msg.From.UserName, ReporterDisplayName: reporterName,
		ReportedUserID: state.target.UserID, ReportedUserName: state.target.UserName,
		ReportedDisplayName: state.target.DisplayName,
		SourceMessageID:     state.sourceMessageID, SourceThreadID: state.sourceThreadID,
		Reason: reason, CreatedAt: time.Now().UTC(),
	}
	if err = a.store.CreateCommunityUserReport(ctx, report); err != nil {
		return fmt.Errorf("store community user report: %w", err)
	}
	a.clearState(msg.From.ID)
	return a.reply(msg.Chat.ID, "Reporte guardado. El equipo podrá verlo en el panel. No se aplicará ninguna sanción automática.")
}

func forwardedCommunityMember(msg *tbapi.Message) (CommunityMember, bool) {
	if msg == nil || msg.ForwardOrigin == nil || !msg.ForwardOrigin.IsUser() ||
		msg.ForwardOrigin.SenderUser == nil {
		return CommunityMember{}, false
	}
	user := msg.ForwardOrigin.SenderUser
	return CommunityMember{
		UserID: user.ID, UserName: user.UserName,
		DisplayName: strings.TrimSpace(strings.Join([]string{user.FirstName, user.LastName}, " ")),
	}, true
}

func privateCommand(text string) (command, argument string) {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", ""
	}
	command = strings.TrimPrefix(strings.ToLower(fields[0]), "/")
	if idx := strings.Index(command, "@"); idx >= 0 {
		command = command[:idx]
	}
	if len(fields) > 1 {
		argument = strings.Join(fields[1:], " ")
	}
	return command, argument
}

func communityMemberLabel(member CommunityMember) string {
	parts := make([]string, 0, 2)
	if name := strings.TrimSpace(member.DisplayName); name != "" {
		parts = append(parts, name)
	}
	if username := strings.TrimPrefix(strings.TrimSpace(member.UserName), "@"); username != "" {
		parts = append(parts, "@"+username)
	}
	if len(parts) == 0 {
		return strconv.FormatInt(member.UserID, 10)
	}
	return strings.Join(parts, " ")
}

func telegramForumMessageURL(chatID int64, threadID, messageID int) string {
	internal := strings.TrimPrefix(strconv.FormatInt(chatID, 10), "-100")
	if threadID > 0 {
		return fmt.Sprintf("https://t.me/c/%s/%d/%d", internal, threadID, messageID)
	}
	return fmt.Sprintf("https://t.me/c/%s/%d", internal, messageID)
}

func newCommunityReportKey() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (a *communityPrivateAssistant) reply(chatID int64, text string) error {
	_, err := a.api.Send(tbapi.NewMessage(chatID, text))
	return err
}

func (a *communityPrivateAssistant) getState(userID int64) (privateReportState, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state, ok := a.states[userID]
	return state, ok
}

func (a *communityPrivateAssistant) setState(userID int64, state privateReportState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.states[userID] = state
}

func (a *communityPrivateAssistant) clearState(userID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.states, userID)
}
