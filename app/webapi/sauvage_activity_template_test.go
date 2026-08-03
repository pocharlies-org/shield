package webapi

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/redstone-md/shield/app/community"
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
