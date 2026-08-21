package model

import (
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// withRouteHealthDB gives each test its own database and a clean process cache,
// so isolation state written by one case cannot leak into the next.
func withRouteHealthDB(t *testing.T) {
	t.Helper()
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ChannelModelHealth{}))
	DB = db
	ClearRouteHealthCache()
	t.Cleanup(func() {
		DB = previousDB
		ClearRouteHealthCache()
	})
}

// withHealthSetting installs a config and restores the previous one, so a
// threshold set here cannot change the defaults another test relies on.
func withHealthSetting(t *testing.T, cfg *operation_setting.ChannelModelHealthSetting) {
	t.Helper()
	previous := operation_setting.GetChannelModelHealthSetting()
	apply := func(c *operation_setting.ChannelModelHealthSetting) {
		for key, value := range map[string]int{
			"CalmFastBase":            c.CalmFastBase,
			"CalmFastInterval":        c.CalmFastInterval,
			"CalmSlowBase":            c.CalmSlowBase,
			"CalmSlowInterval":        c.CalmSlowInterval,
			"DormantBase":             c.DormantBase,
			"DormantInterval":         c.DormantInterval,
			"DormantMaxBase":          c.DormantMaxBase,
			"DormantDisableThreshold": c.DormantDisableThreshold,
		} {
			require.NoError(t, operation_setting.UpdateChannelModelHealthSettingValue(key, strconv.Itoa(value)))
		}
	}
	apply(cfg)
	t.Cleanup(func() { apply(previous) })
}

// TestIsolationDurationLadder pins the four-stage duration ladder from the
// design: three fast calm levels, three slow calm levels, three dormant levels,
// then a flat dormant ceiling. The state name matters as much as the number,
// because dormant expiry is what feeds the auto-disable counter.
func TestIsolationDurationLadder(t *testing.T) {
	cfg := operation_setting.DefaultChannelModelHealthSetting()

	cases := []struct {
		level int
		state string
		want  int64
	}{
		{level: 0, state: HealthHealthy, want: 0},
		{level: 1, state: HealthCalm, want: 3},
		{level: 2, state: HealthCalm, want: 6},
		{level: 3, state: HealthCalm, want: 9},
		{level: 4, state: HealthCalm, want: 20},
		{level: 5, state: HealthCalm, want: 40},
		{level: 6, state: HealthCalm, want: 60},
		{level: 7, state: HealthDormant, want: 120},
		{level: 8, state: HealthDormant, want: 240},
		{level: 9, state: HealthDormant, want: 360},
		{level: 10, state: HealthDormant, want: 360},
		{level: 40, state: HealthDormant, want: 360},
	}
	for _, tc := range cases {
		state, seconds := isolationDuration(tc.level, cfg)
		assert.Equal(t, tc.state, state, "level %d state", tc.level)
		assert.Equal(t, tc.want, seconds, "level %d duration", tc.level)
	}
}

// TestRetryableFailureEscalatesAndExpires covers the core routing contract: a
// retry-eligible failure isolates exactly one (channel, model) route, repeated
// failures climb the ladder, and reaching `until` makes the route selectable
// again without any background probe.
func TestRetryableFailureEscalatesAndExpires(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9001, Model: "gpt-4o"}
	sibling := RouteKey{ChannelId: 9001, Model: "gpt-4o-mini"}

	require.True(t, IsRouteHealthy(key, now), "an unseen route is healthy without a DB read")

	require.NoError(t, RecordRetryableFailure(key, "bad_response_status_code", now))
	assert.False(t, IsRouteHealthy(key, now), "the failed route is isolated")
	assert.True(t, IsRouteHealthy(sibling, now), "isolation must not spill onto another model of the same channel")

	state, healthy := GetRouteHealth(key)
	assert.Equal(t, HealthCalm, state)
	assert.False(t, healthy)

	// Level 1 lasts CalmFastBase=3s, so 3s later the route is selectable again.
	assert.True(t, IsRouteHealthy(key, now.Add(3*time.Second)))

	require.NoError(t, RecordRetryableFailure(key, "bad_response_status_code", now.Add(3*time.Second)))
	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&row).Error)
	assert.Equal(t, 2, row.IsolationLevel, "each retry-eligible failure escalates one level")
	assert.Equal(t, HealthCalm, row.State)
	require.NotNil(t, row.Until)
	assert.Equal(t, now.Add(3*time.Second).Unix()+6, *row.Until, "level 2 lasts 6s")
}

// TestDormantExpiryDisableThreshold covers the auto-disable rule: failing again
// after a dormant window has elapsed counts once, and the route is only disabled
// when a positive threshold is reached. Threshold 0 must cycle forever instead.
func TestDormantExpiryDisableThreshold(t *testing.T) {
	dormantRow := func(t *testing.T, key RouteKey, until int64) {
		t.Helper()
		require.NoError(t, DB.Create(&ChannelModelHealth{
			ChannelId:      key.ChannelId,
			Model:          key.Model,
			State:          HealthDormant,
			IsolationLevel: 9,
			Until:          &until,
			Version:        1,
		}).Error)
	}

	t.Run("threshold reached disables the route", func(t *testing.T) {
		withRouteHealthDB(t)
		cfg := operation_setting.DefaultChannelModelHealthSetting()
		cfg.DormantDisableThreshold = 1
		withHealthSetting(t, cfg)

		now := time.Unix(1_700_000_000, 0)
		key := RouteKey{ChannelId: 9101, Model: "claude-3"}
		dormantRow(t, key, now.Unix()-1)

		require.NoError(t, RecordRetryableFailure(key, "bad_response", now))

		var row ChannelModelHealth
		require.NoError(t, DB.Where("channel_id = ?", key.ChannelId).First(&row).Error)
		assert.Equal(t, HealthDisabled, row.State)
		assert.Equal(t, 1, row.DormantDisableCount)
		assert.Nil(t, row.Until, "a disabled route has no expiry; only an admin restores it")
		assert.False(t, IsRouteHealthy(key, now.Add(24*time.Hour)), "disabled never self-heals")
	})

	t.Run("threshold zero never disables", func(t *testing.T) {
		withRouteHealthDB(t)
		cfg := operation_setting.DefaultChannelModelHealthSetting()
		cfg.DormantDisableThreshold = 0
		withHealthSetting(t, cfg)

		now := time.Unix(1_700_000_000, 0)
		key := RouteKey{ChannelId: 9102, Model: "claude-3"}
		dormantRow(t, key, now.Unix()-1)

		require.NoError(t, RecordRetryableFailure(key, "bad_response", now))

		var row ChannelModelHealth
		require.NoError(t, DB.Where("channel_id = ?", key.ChannelId).First(&row).Error)
		assert.Equal(t, HealthDormant, row.State, "threshold 0 keeps cycling dormant -> healthy")
		assert.Equal(t, 1, row.DormantDisableCount, "the counter still advances for observability")
		require.NotNil(t, row.Until)
		assert.True(t, IsRouteHealthy(key, now.Add(time.Hour)), "the dormant ceiling still expires")
	})
}

// TestAdminRecoverAndDisable pins the two manual transitions: recovery clears the
// ladder and the disable counter, and an admin disable is terminal.
func TestAdminRecoverAndDisable(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9201, Model: "gemini-2.5-pro"}

	require.NoError(t, RecordRetryableFailure(key, "bad_response", now))
	require.NoError(t, RecordRetryableFailure(key, "bad_response", now))
	require.False(t, IsRouteHealthy(key, now))

	require.NoError(t, RecoverRoute(key, now))
	assert.True(t, IsRouteHealthy(key, now), "admin recovery makes the route selectable immediately")

	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ?", key.ChannelId).First(&row).Error)
	assert.Equal(t, HealthHealthy, row.State)
	assert.Zero(t, row.IsolationLevel, "recovery resets the ladder, not just the timer")
	assert.Zero(t, row.DormantDisableCount)

	require.NoError(t, DisableRoute(key, now))
	assert.False(t, IsRouteHealthy(key, now.Add(365*24*time.Hour)))

	rows, err := ListChannelModelHealth(0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, HealthDisabled, rows[0].State)
}

// TestRecordRetryableFailureVersionsMonotonically guards the CAS column: every
// accepted write must bump version, otherwise two instances could overwrite each
// other's isolation silently.
func TestRecordRetryableFailureVersionsMonotonically(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9301, Model: "gpt-4o"}

	seen := map[int]bool{}
	for i := 0; i < 4; i++ {
		require.NoError(t, RecordRetryableFailure(key, "bad_response", now.Add(time.Duration(i)*time.Hour)))
		var row ChannelModelHealth
		require.NoError(t, DB.Where("channel_id = ?", key.ChannelId).First(&row).Error)
		assert.False(t, seen[row.Version], "version %d reused", row.Version)
		seen[row.Version] = true
	}
}
