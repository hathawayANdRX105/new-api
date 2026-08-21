package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// abilityEnabled reads one ability row's enabled flag by its full primary key.
func abilityEnabled(t *testing.T, group, modelName string, channelID int) bool {
	t.Helper()
	var row Ability
	require.NoError(t, DB.Where(commonGroupCol+" = ? and model = ? and channel_id = ?",
		group, modelName, channelID).First(&row).Error)
	return row.Enabled
}

// TestDisableChannelModelBlastRadius pins the reason this helper exists at all:
// the unit of failure is one channel+model pair, not the whole channel. A channel
// serving a dead model plus several healthy ones must keep serving the healthy
// ones, and a model that is dead on one channel must stay available on its other
// channels.
func TestDisableChannelModelBlastRadius(t *testing.T) {
	const group = "disable-group"
	const dead, alive = "model-a", "model-b"

	withAbilityDB(t, group, dead, []Ability{
		ability(9801, group, dead, 10, 100),
		ability(9802, group, dead, 10, 100),
	})
	// withAbilityDB also creates a channel row per ability; the second model on
	// channel 9801 only needs the ability row.
	extra := ability(9801, group, alive, 10, 100)
	require.NoError(t, DB.Create(&extra).Error)

	require.NoError(t, updateAbilityStatusByModelWithTx(DB, 9801, dead, false))

	assert.False(t, abilityEnabled(t, group, dead, 9801),
		"the targeted channel+model pair is the only row disabled")
	assert.True(t, abilityEnabled(t, group, alive, 9801),
		"the same channel's other model must stay available")
	assert.True(t, abilityEnabled(t, group, dead, 9802),
		"the same model on another channel must stay available")
}

// TestDisableChannelModelSpansGroups covers the deliberate cross-group behaviour:
// one channel+model pair appears once per group it is published to, and a model
// that is broken upstream is broken for every group, so all its rows flip.
func TestDisableChannelModelSpansGroups(t *testing.T) {
	const groupA, groupB = "grp-a", "grp-b"
	const modelName = "shared-model"

	withAbilityDB(t, groupA, modelName, []Ability{
		ability(9803, groupA, modelName, 10, 100),
	})
	second := ability(9803, groupB, modelName, 10, 100)
	require.NoError(t, DB.Create(&second).Error)

	require.NoError(t, updateAbilityStatusByModelWithTx(DB, 9803, modelName, false))

	assert.False(t, abilityEnabled(t, groupA, modelName, 9803))
	assert.False(t, abilityEnabled(t, groupB, modelName, 9803),
		"a model broken upstream is broken for every group publishing it")
}

// TestDisableChannelModelRejectsEmptyModel guards the exported wrapper: an empty
// model name would match no rows, so silently succeeding would report a disable
// that never happened.
func TestDisableChannelModelRejectsEmptyModel(t *testing.T) {
	const group, modelName = "guard-group", "guard-model"

	withAbilityDB(t, group, modelName, []Ability{
		ability(9804, group, modelName, 10, 100),
	})

	assert.Error(t, DisableChannelModel(9804, ""))
	assert.True(t, abilityEnabled(t, group, modelName, 9804),
		"a rejected call must not change any row")
}
