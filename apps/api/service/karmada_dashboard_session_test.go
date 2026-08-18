package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKarmadaDashboardSessionPurposeIsolation(t *testing.T) {
	useTestSessionSecret(t)
	identity := AuthIdentity{UserID: 42, SessionID: "session-1", UserAuthVersion: 3, SessionVersion: 2}

	token, expiresAt, err := IssueKarmadaDashboardSession(identity)
	require.NoError(t, err)
	assert.Positive(t, expiresAt)

	access, _, err := IssueAccessToken(identity)
	require.NoError(t, err)
	_, err = ValidateKarmadaDashboardSession(access)
	assert.ErrorIs(t, err, ErrKarmadaDashboardSessionInvalid)

	_, err = ValidateKarmadaDashboardSession(token)
	assert.NotErrorIs(t, err, ErrKarmadaDashboardSessionInvalid)
}

func TestKarmadaDashboardSessionRejectsTampering(t *testing.T) {
	useTestSessionSecret(t)
	identity := AuthIdentity{UserID: 42, SessionID: "session-1", UserAuthVersion: 3, SessionVersion: 2}
	token, _, err := IssueKarmadaDashboardSession(identity)
	require.NoError(t, err)

	tamperAt := len(token) - 2
	replacement := "x"
	if token[tamperAt] == 'x' {
		replacement = "y"
	}
	tampered := token[:tamperAt] + replacement + token[tamperAt+1:]
	_, err = ValidateKarmadaDashboardSession(tampered)
	assert.ErrorIs(t, err, ErrKarmadaDashboardSessionInvalid)
}
