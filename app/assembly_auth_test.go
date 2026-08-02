package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveServerAuthPassword(t *testing.T) {
	tests := []struct {
		name          string
		password      string
		passwordHash  string
		forwardEmails []string
		expected      string
		wantError     bool
	}{
		{
			name:     "explicit password",
			password: "secret",
			expected: "secret",
		},
		{
			name:         "bcrypt hash disables auto password",
			password:     "auto",
			passwordHash: "$2a$12$example",
			expected:     "",
		},
		{
			name:          "trusted forward auth disables auto password",
			password:      "auto",
			forwardEmails: []string{"admin@example.com"},
			expected:      "",
		},
		{
			name:      "unconfigured auto password is rejected",
			password:  "auto",
			wantError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := resolveServerAuthPassword(
				tc.password,
				tc.passwordHash,
				tc.forwardEmails,
			)
			if tc.wantError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.expected, actual)
		})
	}
}
