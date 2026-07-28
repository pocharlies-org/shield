package community

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	tbapi "github.com/OvyFlash/telegram-bot-api"

	"github.com/redstone-md/shield/app/events"
	"github.com/redstone-md/shield/app/storage"
)

// Digest sends a privacy-preserving daily operations summary to the admin chat.
type Digest struct {
	store     *Store
	actions   *storage.ModerationActions
	incidents *storage.IncidentStorage
	bot       events.TbAPI
	adminChat int64
	hour      int
	location  *time.Location
	dashboard string
}

// NewDigest creates a daily community report sender.
func NewDigest(store *Store, actions *storage.ModerationActions, incidents *storage.IncidentStorage,
	bot events.TbAPI, adminChat int64,
	hour int, timezone, dashboardURL string,
) (*Digest, error) {
	if store == nil || actions == nil || incidents == nil || bot == nil || adminChat == 0 {
		return nil, fmt.Errorf("community digest requires community, action and incident stores, bot, and numeric admin chat")
	}
	if hour < 0 || hour > 23 {
		return nil, fmt.Errorf("community digest hour must be between 0 and 23")
	}
	location, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil {
		return nil, fmt.Errorf("load community digest timezone: %w", err)
	}
	return &Digest{
		store: store, actions: actions, incidents: incidents, bot: bot, adminChat: adminChat, hour: hour,
		location: location, dashboard: strings.TrimRight(strings.TrimSpace(dashboardURL), "/"),
	}, nil
}

// Run schedules one digest at the configured local hour every day.
func (d *Digest) Run(ctx context.Context) {
	for {
		now := time.Now().In(d.location)
		next := time.Date(now.Year(), now.Month(), now.Day(), d.hour, 0, 0, 0, d.location)
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if err := d.SendNow(ctx); err != nil {
				log.Printf("[WARN] failed to send community daily digest: %v", err)
			}
		}
	}
}

// SendNow builds and sends a report for the previous 24 hours.
func (d *Digest) SendNow(ctx context.Context) error {
	since := time.Now().UTC().Add(-24 * time.Hour)
	snapshot, err := d.store.Dashboard(ctx, since, 10)
	if err != nil {
		return err
	}
	actionSummary, err := d.actions.Summary(ctx, since)
	if err != nil {
		return err
	}
	incidentSummary, err := d.incidents.DashboardSummary(ctx, since)
	if err != nil {
		return err
	}
	s := snapshot.Summary
	text := fmt.Sprintf(
		"Informe diario de Sauvage\n\n"+
			"Decisiones de topics: %d\n"+
			"Infracciones simuladas: %d\n"+
			"Infracciones activas: %d\n"+
			"Warnings / restricciones / expulsiones: %d / %d / %d\n"+
			"Incidentes Ornith / pendientes / críticos: %d / %d / %d\n"+
			"Acciones Telegram correctas / fallidas: %d / %d\n"+
			"Presentaciones acumuladas: %d\n"+
			"Participaciones acumuladas: %d",
		s.TotalEvents, s.ShadowViolations, s.LiveViolations, s.Warnings, s.Restrictions, s.Bans,
		incidentSummary.LLM, incidentSummary.Open, incidentSummary.Critical,
		actionSummary.Completed, actionSummary.Failed, s.Presentations, s.ContestEntries,
	)
	if d.dashboard != "" {
		text += "\n\nPanel: " + d.dashboard
	}
	msg := tbapi.NewMessage(d.adminChat, text)
	msg.LinkPreviewOptions = tbapi.LinkPreviewOptions{IsDisabled: true}
	if _, err = d.bot.Send(msg); err != nil {
		return fmt.Errorf("send community digest: %w", err)
	}
	return nil
}
