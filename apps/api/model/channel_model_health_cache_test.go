package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitChannelModelHealthCacheLoadsPersistedIsolation(t *testing.T) {
	withRouteHealthDB(t)

	now := time.Now()
	until := now.Add(time.Minute).Unix()
	require.NoError(t, DB.Create(&ChannelModelHealth{
		ChannelId:      9401,
		Model:          "startup-cache-model",
		State:          HealthCalm,
		IsolationLevel: 2,
		Until:          &until,
		Version:        4,
	}).Error)
	ClearRouteHealthCache()

	InitChannelModelHealthCache()

	assert.False(t, IsRouteHealthy(RouteKey{ChannelId: 9401, Model: "startup-cache-model"}, now))
	state, healthy := GetRouteHealth(RouteKey{ChannelId: 9401, Model: "startup-cache-model"})
	assert.Equal(t, HealthCalm, state)
	assert.False(t, healthy)
}
