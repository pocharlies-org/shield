package webapi

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSauvageContestsTemplateUsesMouseDatePicker(t *testing.T) {
	templateBody, err := templateFS.ReadFile("assets/sauvage_contests.html")
	require.NoError(t, err)
	appJS, err := templateFS.ReadFile("assets/app.js")
	require.NoError(t, err)

	html := string(templateBody)
	assert.Contains(t, html, `type="date" name="deadline"`)
	assert.Contains(t, html, `data-date-picker-trigger="contest-deadline-date"`)
	assert.Contains(t, html, `Abrir calendario`)
	assert.Contains(t, html, `type="time" name="deadline_time" value="23:59"`)
	assert.NotContains(t, html, `type="datetime-local"`)
	assert.Contains(t, string(appJS), `input.showPicker()`)
}

func TestParseSauvageContestDeadlineFromDatePicker(t *testing.T) {
	deadline, err := parseSauvageContestDeadline("2026-08-31", "21:30")
	require.NoError(t, err)
	require.NotNil(t, deadline)
	expected := time.Date(2026, time.August, 31, 21, 30, 0, 0, time.Local).UTC()
	assert.Equal(t, expected, *deadline)
}

func TestParseSauvageContestDeadlineDefaultsToEndOfDay(t *testing.T) {
	deadline, err := parseSauvageContestDeadline("2026-08-31", "")
	require.NoError(t, err)
	require.NotNil(t, deadline)
	expected := time.Date(2026, time.August, 31, 23, 59, 0, 0, time.Local).UTC()
	assert.Equal(t, expected, *deadline)
}

func TestParseSauvageContestDeadlineKeepsLegacyDatetimeLocal(t *testing.T) {
	deadline, err := parseSauvageContestDeadline("2026-08-31T18:45", "23:59")
	require.NoError(t, err)
	require.NotNil(t, deadline)
	expected := time.Date(2026, time.August, 31, 18, 45, 0, 0, time.Local).UTC()
	assert.Equal(t, expected, *deadline)
}

func TestParseSauvageContestDeadlineRejectsInvalidValues(t *testing.T) {
	deadline, err := parseSauvageContestDeadline("31/08/2026", "21:30")
	require.Error(t, err)
	assert.Nil(t, deadline)
}

func TestParseSauvageContestDeadlineIsOptional(t *testing.T) {
	deadline, err := parseSauvageContestDeadline("", "23:59")
	require.NoError(t, err)
	assert.Nil(t, deadline)
}
