package webapi

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/redstone-md/shield/app/community"
)

func TestSauvagePresentationsTemplateOffersUserAutocomplete(t *testing.T) {
	var rendered bytes.Buffer
	err := tmpl.ExecuteTemplate(&rendered, "sauvage_presentations.html", sauvagePresentationsView{
		Filter: community.PresentationFilter{UserQuery: "alberto"},
	})
	require.NoError(t, err)

	html := rendered.String()
	assert.Contains(t, html, `name="user" value="alberto"`)
	assert.Contains(t, html, `list="presentation-user-options"`)
	assert.Contains(t, html, `fetch('/sauvage/presentations/suggestions?q='`)
	assert.Contains(t, html, `option.label = suggestion.label`)
}

func TestNewSauvagePresentationUserSuggestion(t *testing.T) {
	suggestion := newSauvagePresentationUserSuggestion(community.PresentationRecord{
		UserID: 1425068805, UserName: "@albertob23", DisplayName: " Alberto ",
	})

	assert.Equal(t, "@albertob23", suggestion.Value)
	assert.Equal(t, "Alberto (@albertob23) · ID 1425068805", suggestion.Label)
	assert.Equal(t, "albertob23", suggestion.UserName)
	assert.Equal(t, "Alberto", suggestion.DisplayName)
}
