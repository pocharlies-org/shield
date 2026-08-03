package webapi

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/redstone-md/shield/app/community"
	"github.com/redstone-md/shield/app/storage"
)

func TestSauvageActivityTemplatePreservesFilters(t *testing.T) {
	var rendered bytes.Buffer
	err := tmpl.ExecuteTemplate(&rendered, "sauvage_activity.html", sauvageActivityView{
		Filter: community.RuleEventFilter{
			ThreadID: 3,
			RuleCode: "presentation_duplicate",
			Action:   "restrict",
		},
		Days: 14,
		Mode: "shadow",
	})
	require.NoError(t, err)

	html := rendered.String()
	assert.Contains(t, html, `name="days" min="1" max="90" value="14"`)
	assert.Contains(t, html, `value="3" selected>Presentaciones`)
	assert.Contains(t, html, `name="rule" value="presentation_duplicate"`)
	assert.Contains(t, html, `value="restrict" selected>restrict`)
	assert.Contains(t, html, `value="shadow" selected>Shadow`)
}

func TestSauvageActivityTemplateShowsDetailedTelegramAction(t *testing.T) {
	var rendered bytes.Buffer
	err := tmpl.ExecuteTemplate(&rendered, "sauvage_activity.html", sauvageActivityView{
		Actions: []storage.ModerationActionEntry{{
			Command: "warn_user", Status: "failed", ChatID: -1003672565710,
			TargetUserID: 1425068805, UserName: "albertob23", DisplayName: "Alberto",
			SourceMessageID: 52926, ThreadID: 3, MessageText: "Mensaje literal",
			ReasonCode: "llm_openai", Description: "Descripción concreta", LastError: "Telegram error",
			Attempt: 2, CorrelationID: "corr-1", CreatedAt: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC),
		}},
	})
	require.NoError(t, err)

	html := rendered.String()
	assert.Contains(t, html, `href="/sauvage/users/1425068805">Alberto</a>`)
	assert.Contains(t, html, `@albertob23`)
	assert.Contains(t, html, `https://t.me/c/3672565710/3/52926`)
	assert.Contains(t, html, `Mensaje literal`)
	assert.Contains(t, html, `Descripción concreta`)
	assert.Contains(t, html, `Telegram error`)
}
