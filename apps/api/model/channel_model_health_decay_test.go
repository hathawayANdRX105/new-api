package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedHealthRowWithLevel seeds a row with explicit state/level/until/version
// and mirrors it into the process cache, for RecordSuccess and expiry tests.
func seedHealthRowWithLevel(t *testing.T, key RouteKey, state string, level int, until *int64, version int) {
	t.Helper()
	row := ChannelModelHealth{
		ChannelId:      key.ChannelId,
		KeyIndex:       key.KeyIndex,
		Model:          key.Model,
		State:          state,
		IsolationLevel: level,
		Until:          until,
		Version:        version,
	}
	require.NoError(t, DB.Create(&row).Error)
	cacheHealth(&row)
}

// TestRecordSuccessDecaysLevel verifies that a single successful request
// decrements isolation_level by decayStep (NormalDecayStep=1 at normal
// pressure) and stamps last_success_at. Fails if: the level does not decrease,
// last_success_at is not set, or the CAS version does not advance.
func TestRecordSuccessDecaysLevel(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9601, KeyIndex: 0, Model: "success-decay"}

	// Seed a calm row at level 3.
	deadline := now.Add(time.Hour).Unix()
	seedHealthRowWithLevel(t, key, HealthCalm, 3, &deadline, 1)

	require.NoError(t, RecordSuccess(key, now.Add(time.Minute)))

	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND key_index = ? AND model = ?", key.ChannelId, key.KeyIndex, key.Model).First(&row).Error)
	assert.Equal(t, 2, row.IsolationLevel, "level should decay by 1 (NormalDecayStep)")
	assert.Equal(t, HealthCalm, row.State, "level 2 is still calm")
	assert.Equal(t, 2, row.Version, "version must advance")
	require.NotNil(t, row.LastSuccessAt, "last_success_at must be stamped")
	assert.Equal(t, now.Add(time.Minute).Unix(), *row.LastSuccessAt)

	// Cache must reflect the decayed level.
	_, level, _, ok := GetRouteIsolation(key)
	require.True(t, ok)
	assert.Equal(t, 2, level, "cache must match DB after RecordSuccess")
}

// TestRecordSuccessReachesHealthy verifies that when the decayed level reaches
// 0, the route returns to healthy with until cleared. Fails if: the state
// remains isolated, until is not nil, or the level goes negative.
func TestRecordSuccessReachesHealthy(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9602, KeyIndex: 0, Model: "success-to-healthy"}

	// Seed a calm row at level 1 — one success decays to 0 → healthy.
	deadline := now.Add(time.Hour).Unix()
	seedHealthRowWithLevel(t, key, HealthCalm, 1, &deadline, 1)

	require.NoError(t, RecordSuccess(key, now))

	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND key_index = ? AND model = ?", key.ChannelId, key.KeyIndex, key.Model).First(&row).Error)
	assert.Equal(t, 0, row.IsolationLevel, "level should reach 0")
	assert.Equal(t, HealthHealthy, row.State, "state should be healthy")
	assert.Nil(t, row.Until, "until should be cleared")
	assert.Equal(t, 2, row.Version)

	// Cache must reflect healthy.
	state, level, _, ok := GetRouteIsolation(key)
	require.True(t, ok)
	assert.Equal(t, HealthHealthy, state)
	assert.Equal(t, 0, level)
}

// TestRecordSuccessDisabledImmune verifies that a disabled route is immune to
// success-driven decay: no state, level, or version change. Fails if: the
// disabled row is mutated in any way.
func TestRecordSuccessDisabledImmune(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9603, KeyIndex: 0, Model: "disabled-immune"}

	seedHealthRowWithLevel(t, key, HealthDisabled, 7, nil, 1)

	require.NoError(t, RecordSuccess(key, now))

	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND key_index = ? AND model = ?", key.ChannelId, key.KeyIndex, key.Model).First(&row).Error)
	assert.Equal(t, HealthDisabled, row.State, "disabled must not change")
	assert.Equal(t, 7, row.IsolationLevel, "level must not decay")
	assert.Equal(t, 1, row.Version, "version must not advance")
}

// TestRecordSuccessMissingRowNoop verifies that a success for a route with no
// DB row is treated as already healthy — no row is created. Fails if: a new
// row is inserted, since a success must not conjure an isolation record.
func TestRecordSuccessMissingRowNoop(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9604, KeyIndex: 0, Model: "no-row"}

	require.NoError(t, RecordSuccess(key, now))

	var count int64
	require.NoError(t, DB.Model(&ChannelModelHealth{}).Where("channel_id = ? AND key_index = ? AND model = ?", key.ChannelId, key.KeyIndex, key.Model).Count(&count).Error)
	assert.Zero(t, count, "no row should be created for a success on an unseen route")
	assert.True(t, IsRouteHealthy(key, now), "missing row = healthy")
}

// TestRecordSuccessClearsDormantCountInCalmBand verifies that when the decayed
// level falls into the calm band (<=6), the dormant_disable_count is zeroed,
// fixing the v1 bug where the counter only ever increased. Fails if: the
// dormant count is non-zero after decaying from dormant (level >6) to calm
// (level <=6).
func TestRecordSuccessClearsDormantCountInCalmBand(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9605, KeyIndex: 0, Model: "dormant-count-clear"}

	// Seed a dormant row at level 7 with dormant_disable_count=2.
	deadline := now.Add(time.Hour).Unix()
	seedHealthRowWithLevel(t, key, HealthDormant, 7, &deadline, 1)

	// Manually set dormant_disable_count to 2 via DB.
	require.NoError(t, DB.Model(&ChannelModelHealth{}).
		Where("channel_id = ? AND key_index = ? AND model = ?", key.ChannelId, key.KeyIndex, key.Model).
		Update("dormant_disable_count", 2).Error)

	// One success: level 7 → 6 (calm band), dormant count should clear.
	require.NoError(t, RecordSuccess(key, now))

	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND key_index = ? AND model = ?", key.ChannelId, key.KeyIndex, key.Model).First(&row).Error)
	assert.Equal(t, 6, row.IsolationLevel, "level should decay to 6")
	assert.Equal(t, 0, row.DormantDisableCount, "dormant count should be cleared in calm band")
}

// TestRecordSuccessDormantCountPreservedAboveCalmBand verifies that when the
// decayed level stays above the calm band (>6), the dormant_disable_count is
// preserved. Fails if: the dormant count is cleared when it should not be.
func TestRecordSuccessDormantCountPreservedAboveCalmBand(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9606, KeyIndex: 0, Model: "dormant-count-preserved"}

	// Seed a dormant row at level 9 with dormant_disable_count=2.
	deadline := now.Add(time.Hour).Unix()
	seedHealthRowWithLevel(t, key, HealthDormant, 9, &deadline, 1)

	require.NoError(t, DB.Model(&ChannelModelHealth{}).
		Where("channel_id = ? AND key_index = ? AND model = ?", key.ChannelId, key.KeyIndex, key.Model).
		Update("dormant_disable_count", 2).Error)

	// One success: level 9 → 8 (still dormant, >6), dormant count preserved.
	require.NoError(t, RecordSuccess(key, now))

	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND key_index = ? AND model = ?", key.ChannelId, key.KeyIndex, key.Model).First(&row).Error)
	assert.Equal(t, 8, row.IsolationLevel, "level should decay to 8")
	assert.Equal(t, 2, row.DormantDisableCount, "dormant count preserved above calm band")
}

// TestExpiryCASDecaysLevel verifies that the lazy expiry CAS in IsRouteHealthy
// decrements isolation_level (not just clears state/until). Fails if: the
// level stays unchanged after expiry, proving the CAS does not decay.
func TestExpiryCASDecaysLevel(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9607, KeyIndex: 0, Model: "expiry-decay"}

	// Seed a calm row at level 3 with an already-expired deadline.
	expired := now.Add(-time.Minute).Unix()
	seedHealthRowWithLevel(t, key, HealthCalm, 3, &expired, 5)

	// IsRouteHealthy triggers the expiry CAS, which should decay the level.
	assert.True(t, IsRouteHealthy(key, now))

	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND key_index = ? AND model = ?", key.ChannelId, key.KeyIndex, key.Model).First(&row).Error)
	assert.Equal(t, HealthHealthy, row.State, "expiry CAS should set healthy")
	assert.Nil(t, row.Until, "expiry CAS should clear until")
	assert.Equal(t, 2, row.IsolationLevel, "level should decay by 1 (NormalDecayStep)")
	assert.Equal(t, 6, row.Version, "version must advance")
}

// TestExpiryCASClearsDormantCountInCalmBand verifies that the expiry CAS
// zeroes dormant_disable_count when the decayed level falls into the calm
// band (<=6). Fails if: the dormant count survives expiry into the calm band.
func TestExpiryCASClearsDormantCountInCalmBand(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9608, KeyIndex: 0, Model: "expiry-dormant-clear"}

	// Seed a dormant row at level 7 with dormant_disable_count=3, expired.
	expired := now.Add(-time.Minute).Unix()
	seedHealthRowWithLevel(t, key, HealthDormant, 7, &expired, 1)

	require.NoError(t, DB.Model(&ChannelModelHealth{}).
		Where("channel_id = ? AND key_index = ? AND model = ?", key.ChannelId, key.KeyIndex, key.Model).
		Update("dormant_disable_count", 3).Error)

	// Re-cache the updated dormant count.
	var refreshed ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND key_index = ? AND model = ?", key.ChannelId, key.KeyIndex, key.Model).First(&refreshed).Error)
	cacheHealth(&refreshed)

	// Expiry CAS: level 7 → 6 (calm band), dormant count should clear.
	assert.True(t, IsRouteHealthy(key, now))

	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND key_index = ? AND model = ?", key.ChannelId, key.KeyIndex, key.Model).First(&row).Error)
	assert.Equal(t, 6, row.IsolationLevel, "level should decay to 6")
	assert.Equal(t, 0, row.DormantDisableCount, "dormant count should be cleared in calm band")
	assert.Equal(t, HealthHealthy, row.State)

	// Cache must match DB.
	_, level, _, ok := GetRouteIsolation(key)
	require.True(t, ok)
	assert.Equal(t, 6, level, "cache level must match DB")
}

// TestExpiryCASDormantCountPreservedAboveCalmBand verifies that the expiry CAS
// preserves dormant_disable_count when the decayed level stays above the calm
// band (>6). Fails if: the dormant count is cleared when it should not be.
func TestExpiryCASDormantCountPreservedAboveCalmBand(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9609, KeyIndex: 0, Model: "expiry-dormant-preserved"}

	// Seed a dormant row at level 9 with dormant_disable_count=2, expired.
	expired := now.Add(-time.Minute).Unix()
	seedHealthRowWithLevel(t, key, HealthDormant, 9, &expired, 1)

	require.NoError(t, DB.Model(&ChannelModelHealth{}).
		Where("channel_id = ? AND key_index = ? AND model = ?", key.ChannelId, key.KeyIndex, key.Model).
		Update("dormant_disable_count", 2).Error)

	// Re-cache the updated dormant count.
	var refreshed ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND key_index = ? AND model = ?", key.ChannelId, key.KeyIndex, key.Model).First(&refreshed).Error)
	cacheHealth(&refreshed)

	// Expiry CAS: level 9 → 8 (still dormant, >6), dormant count preserved.
	assert.True(t, IsRouteHealthy(key, now))

	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND key_index = ? AND model = ?", key.ChannelId, key.KeyIndex, key.Model).First(&row).Error)
	assert.Equal(t, 8, row.IsolationLevel, "level should decay to 8")
	assert.Equal(t, 2, row.DormantDisableCount, "dormant count preserved above calm band")
	assert.Equal(t, HealthHealthy, row.State)
}
