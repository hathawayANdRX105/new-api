package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedHealthRow inserts a ChannelModelHealth row and mirrors it into the process
// cache, so the test exercises both the DB and the in-memory fast path. The
// caller picks State / IsolationLevel / Version to match the invariant under
// test; Until defaults to a far-future deadline so an isolated row stays
// isolated throughout the test window.
func seedHealthRow(t *testing.T, channelID int, model, state string, level, version int) {
	t.Helper()
	until := time.Now().Add(time.Hour).Unix()
	row := ChannelModelHealth{
		ChannelId:      channelID,
		Model:          model,
		State:          state,
		IsolationLevel: level,
		Until:          &until,
		Version:        version,
	}
	require.NoError(t, DB.Create(&row).Error)
	cacheHealth(&row)
}

// assertRouteIsolated checks that a route is currently calm and unhealthy in
// both cache and DB, i.e. the isolation state was preserved untouched.
func assertRouteIsolated(t *testing.T, channelID int, model, state string, level, version int) {
	t.Helper()
	key := RouteKey{ChannelId: channelID, Model: model}
	assert.False(t, IsRouteHealthy(key, time.Now()),
		"route (%d,%s) should still be isolated", channelID, model)
	st, healthy := GetRouteHealth(key)
	assert.Equal(t, state, st, "cached state for (%d,%s) should be %s", channelID, model, state)
	assert.False(t, healthy, "GetRouteHealth should report unhealthy for (%d,%s)", channelID, model)
	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", channelID, model).First(&row).Error)
	assert.Equal(t, state, row.State, "DB state for (%d,%s) should be %s", channelID, model, state)
	assert.Equal(t, level, row.IsolationLevel, "DB isolation level for (%d,%s) should be %d", channelID, model, level)
	assert.Equal(t, version, row.Version, "DB version for (%d,%s) should be %d", channelID, model, version)
}

// assertRouteGone checks that a route has no DB row and no cache entry (the
// healthy default), proving the cleanup function reached both stores.
func assertRouteGone(t *testing.T, channelID int, model string) {
	t.Helper()
	key := RouteKey{ChannelId: channelID, Model: model}
	assert.True(t, IsRouteHealthy(key, time.Now()),
		"route (%d,%s) should be healthy after cleanup (cache miss)", channelID, model)
	st, healthy := GetRouteHealth(key)
	assert.Equal(t, HealthHealthy, st, "GetRouteHealth should return healthy for (%d,%s) after cleanup", channelID, model)
	assert.True(t, healthy, "GetRouteHealth should return healthy=true for (%d,%s) after cleanup", channelID, model)
	var count int64
	require.NoError(t, DB.Model(&ChannelModelHealth{}).Where("channel_id = ? AND model = ?", channelID, model).Count(&count).Error)
	assert.Zero(t, count, "DB should have no rows for (%d,%s) after cleanup", channelID, model)
}

// ---- A. deleteRouteHealthByChannelIDsWithTx: clears DB rows and cache ----

// TestDeleteRouteHealthByChannelIDsClearsRowsAndCache verifies that deleting
// by channel ID removes both the persisted isolation rows and the in-memory
// cache entries for those channels, while leaving sibling channels untouched.
// Fails if: the function only deletes DB rows but forgets the cache (ghost
// isolation suppresses a healthy route), or vice-versa, or if it over-deletes
// into other channels.
func TestDeleteRouteHealthByChannelIDsClearsRowsAndCache(t *testing.T) {
	withRouteHealthDB(t)

	seedHealthRow(t, 8801, "model-alpha", HealthCalm, 2, 3)
	seedHealthRow(t, 8801, "model-beta", HealthCalm, 2, 3)
	seedHealthRow(t, 8802, "model-gamma", HealthCalm, 2, 3)

	require.NoError(t, deleteRouteHealthByChannelIDsWithTx(DB, []int{8801}))

	// 8801 routes must be gone from both DB and cache.
	assertRouteGone(t, 8801, "model-alpha")
	assertRouteGone(t, 8801, "model-beta")

	// 8802 must be untouched: the delete must not bleed into other channels.
	assertRouteIsolated(t, 8802, "model-gamma", HealthCalm, 2, 3)
}

// TestDeleteRouteHealthByChannelIDsNilIDsIsNoOp verifies that a nil/empty ID
// list is a no-op — the guard clause prevents a `WHERE channel_id IN ()` SQL
// error or an accidental full-table cache wipe.
// Fails if: the early-return guard is removed and the empty-slice case
// generates invalid SQL or deletes all cached entries.
func TestDeleteRouteHealthByChannelIDsNilIDsIsNoOp(t *testing.T) {
	withRouteHealthDB(t)

	seedHealthRow(t, 8803, "model-delta", HealthCalm, 1, 1)

	require.NoError(t, deleteRouteHealthByChannelIDsWithTx(DB, nil))

	// Row and cache must survive the no-op call.
	assertRouteIsolated(t, 8803, "model-delta", HealthCalm, 1, 1)
}

// ---- B. deleteRouteHealthNotInModelsWithTx: preserve semantics ----

// TestDeleteRouteHealthNotInModelsPreservesKeptRows is the most critical
// invariant: editing a channel's model list must delete isolation rows for
// removed models while preserving the State / IsolationLevel / Version of
// every surviving model, in both DB and cache. A delete-and-rebuild approach
// would reset the isolation ladder and silently re-enable an unhealthy route.
func TestDeleteRouteHealthNotInModelsPreservesKeptRows(t *testing.T) {
	withRouteHealthDB(t)

	cases := []struct {
		name       string
		keepModels []string
	}{
		{
			name:       "keep one, remove one",
			keepModels: []string{"kept-a"},
		},
		{
			name:       "nil list equals full channel clear",
			keepModels: nil,
		},
		{
			name:       "empty strings and duplicates are ignored",
			keepModels: []string{"", "kept-a", "kept-a"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Each sub-case starts from a clean DB and cache. The fixture
			// (withRouteHealthDB) only runs once at the top level, so we must
			// manually purge state between sub-cases.
			require.NoError(t, DB.Where("channel_id = ?", 8804).Delete(&ChannelModelHealth{}).Error)
			ClearRouteHealthCache()

			seedHealthRow(t, 8804, "kept-a", HealthCalm, 2, 3)
			seedHealthRow(t, 8804, "removed-b", HealthCalm, 1, 2)

			require.NoError(t, deleteRouteHealthNotInModelsWithTx(DB, 8804, tc.keepModels))

			if tc.keepModels == nil {
				// nil → full channel clear: both routes must be gone.
				assertRouteGone(t, 8804, "kept-a")
				assertRouteGone(t, 8804, "removed-b")
				return
			}

			// kept-a must survive with State / IsolationLevel / Version intact.
			assertRouteIsolated(t, 8804, "kept-a", HealthCalm, 2, 3)
			// removed-b must be gone from DB and cache.
			assertRouteGone(t, 8804, "removed-b")
		})
	}
}

// ---- C. Channel.deleteWithTx: end-to-end channel deletion ----

// TestChannelDeleteWithTxCleansRouteHealth verifies the end-to-end path from
// channel deletion down to isolation-row cleanup. We call (*Channel).deleteWithTx
// directly (passing DB as the tx) instead of Channel.Delete() because Delete()
// wraps the call in MutateGatewayRouting, which requires the
// gateway_config_revisions and gateway_config_outboxes tables — unnecessary
// weight for this invariant. The deleteWithTx layer is where the
// deleteRouteHealthByChannelIDsWithTx wiring lives, so it is the right seam.
// Fails if: the deleteWithTx function is missing the route-health cleanup
// call, leaving ghost rows behind after a channel is removed.
func TestChannelDeleteWithTxCleansRouteHealth(t *testing.T) {
	withRouteHealthDB(t)
	require.NoError(t, DB.AutoMigrate(&Channel{}, &Ability{}))

	ch := Channel{
		Id:     8805,
		Type:   1,
		Key:    "test-key",
		Status: common.ChannelStatusEnabled,
		Name:   "cleanup-test-channel",
		Models: "model-a",
		Group:  "default",
	}
	require.NoError(t, DB.Create(&ch).Error)
	require.NoError(t, DB.Create(&Ability{
		Group:     "default",
		Model:     "model-a",
		ChannelId: ch.Id,
		Enabled:   true,
	}).Error)
	seedHealthRow(t, ch.Id, "model-a", HealthCalm, 2, 3)

	require.NoError(t, ch.deleteWithTx(DB))

	var count int64
	require.NoError(t, DB.Model(&ChannelModelHealth{}).Where("channel_id = ?", ch.Id).Count(&count).Error)
	assert.Zero(t, count, "no isolation rows should remain after channel deletion")
}

// ---- D. Channel.UpdateAbilities: model-list edit end-to-end ----

// TestUpdateAbilitiesRemovesOrphanedRouteHealth verifies that when a channel's
// model list is narrowed (e.g. "a,b" → "a"), UpdateAbilities deletes the
// isolation rows for the removed model while preserving the isolation state
// of the kept model. We pass DB directly to UpdateAbilities; when tx == nil it
// creates its own transaction internally (ability.go:276-281). On SQLite the
// in-process shared-cache memory DB handles nested Begin/Commit fine, so no
// manual transaction is needed.
// Fails if: UpdateAbilities is missing the deleteRouteHealthNotInModelsWithTx
// call, leaving ghost rows for models the channel no longer serves.
func TestUpdateAbilitiesRemovesOrphanedRouteHealth(t *testing.T) {
	withRouteHealthDB(t)
	require.NoError(t, DB.AutoMigrate(&Channel{}, &Ability{}))

	ch := Channel{
		Id:     8806,
		Type:   1,
		Key:    "test-key",
		Status: common.ChannelStatusEnabled,
		Name:   "edit-models-channel",
		Models: "a,b",
		Group:  "default",
	}
	require.NoError(t, DB.Create(&ch).Error)

	seedHealthRow(t, ch.Id, "a", HealthCalm, 2, 3)
	seedHealthRow(t, ch.Id, "b", HealthCalm, 1, 2)

	// Narrow the model list: "a,b" → "a". Model "b" is now orphaned.
	ch.Models = "a"
	require.NoError(t, ch.UpdateAbilities(DB))

	// "a" must keep its isolation state untouched.
	assertRouteIsolated(t, ch.Id, "a", HealthCalm, 2, 3)
	// "b" must be gone from DB and cache.
	assertRouteGone(t, ch.Id, "b")
}

// ---- E. Transaction rollback: cache eviction is one-directional ----

// TestRouteHealthCacheEvictionSurvivesRollback verifies the intentional safety
// property documented on deleteRouteHealthByChannelIDsWithTx: the cache is
// evicted *inside* the transaction, so even if the outer transaction rolls
// back, the cache stays cleared. After rollback the DB row reappears, but the
// cache miss causes IsRouteHealthy to return true (healthy default). This is
// the safe direction: a cache miss is treated as healthy, so the worst outcome
// is a route serving traffic it would otherwise be isolated from — never a
// healthy route being suppressed. This is a deliberate design choice, not a
// defect.
//
// IsRouteHealthy returns true on cache miss without touching the DB (confirmed
// in channel_model_health.go:52-58: nil state → return true, no DB read).
func TestRouteHealthCacheEvictionSurvivesRollback(t *testing.T) {
	withRouteHealthDB(t)

	seedHealthRow(t, 8807, "model-epsilon", HealthCalm, 2, 3)

	tx := DB.Begin()
	require.NoError(t, deleteRouteHealthByChannelIDsWithTx(tx, []int{8807}))
	// Rollback the DB delete — the row must reappear in the DB.
	require.NoError(t, tx.Rollback().Error)

	// The DB row must survive the rollback.
	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", 8807, "model-epsilon").First(&row).Error)
	assert.Equal(t, HealthCalm, row.State, "DB row should survive transaction rollback")
	assert.Equal(t, 2, row.IsolationLevel, "DB isolation level should survive rollback")
	assert.Equal(t, 3, row.Version, "DB version should survive rollback")

	// But the cache was already evicted inside the (rolled-back) transaction.
	// IsRouteHealthy sees a cache miss and returns true — the healthy default.
	// This is the safe direction: cache miss = healthy, never the reverse.
	key := RouteKey{ChannelId: 8807, Model: "model-epsilon"}
	assert.True(t, IsRouteHealthy(key, time.Now()),
		"cache miss after rollback must report healthy (safe direction: never suppress a healthy route)")
	st, healthy := GetRouteHealth(key)
	assert.Equal(t, HealthHealthy, st, "GetRouteHealth must return healthy default on cache miss after rollback")
	assert.True(t, healthy, "GetRouteHealth must return healthy=true on cache miss after rollback")
}

func TestDeleteRouteHealthOutsideKeyRangeClearsOrphanedKeys(t *testing.T) {
	withRouteHealthDB(t)

	seedHealthRow(t, 8808, "key-model", HealthCalm, 2, 3)
	keyOne := RouteKey{ChannelId: 8808, KeyIndex: 1, Model: "key-model"}
	until := time.Now().Add(time.Hour).Unix()
	row := ChannelModelHealth{
		ChannelId:      keyOne.ChannelId,
		KeyIndex:       keyOne.KeyIndex,
		Model:          keyOne.Model,
		State:          HealthCalm,
		IsolationLevel: 2,
		Until:          &until,
		Version:        3,
	}
	require.NoError(t, DB.Create(&row).Error)
	cacheHealth(&row)

	require.NoError(t, deleteRouteHealthOutsideKeyRangeWithTx(DB, 8808, 1))
	assertRouteIsolated(t, 8808, "key-model", HealthCalm, 2, 3)
	assert.True(t, IsRouteHealthy(keyOne, time.Now()))

	var count int64
	require.NoError(t, DB.Model(&ChannelModelHealth{}).Where("channel_id = ? AND key_index = ?", keyOne.ChannelId, keyOne.KeyIndex).Count(&count).Error)
	assert.Zero(t, count)
}
