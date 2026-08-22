package model

import (
	"strconv"
	"sync"
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
			"CalmFastBase":             c.CalmFastBase,
			"CalmFastInterval":         c.CalmFastInterval,
			"CalmSlowBase":             c.CalmSlowBase,
			"CalmSlowInterval":         c.CalmSlowInterval,
			"DormantBase":              c.DormantBase,
			"DormantInterval":          c.DormantInterval,
			"DormantMaxBase":           c.DormantMaxBase,
			"DormantDisableThreshold":  c.DormantDisableThreshold,
			"LocalFailureThreshold":    c.LocalFailureThreshold,
			"UpstreamFailureThreshold": c.UpstreamFailureThreshold,
			"CalmWeightScale":          c.CalmWeightScale,
			"DormantWeightScale":       c.DormantWeightScale,
			"EmergencyThreshold":       c.EmergencyThreshold,
			"WarningThreshold":         c.WarningThreshold,
			"AcceleratedDecayStep":     c.AcceleratedDecayStep,
			"NormalDecayStep":          c.NormalDecayStep,
		} {
			require.NoError(t, operation_setting.UpdateChannelModelHealthSettingValue(key, strconv.Itoa(value)))
		}
		require.NoError(t, operation_setting.UpdateChannelModelHealthSettingValue("KeyProbeEnabled", strconv.FormatBool(c.KeyProbeEnabled)))
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

	require.NoError(t, RecordRetryableFailure(key, "bad_response_status_code", FailureSourceUpstream, now))
	assert.False(t, IsRouteHealthy(key, now), "the failed route is isolated")
	assert.True(t, IsRouteHealthy(sibling, now), "isolation must not spill onto another model of the same channel")

	state, healthy := GetRouteHealth(key)
	assert.Equal(t, HealthCalm, state)
	assert.False(t, healthy)

	// Level 1 lasts CalmFastBase=3s, so 3s later the route is selectable again.
	// That elapsed window also decays the ladder back to level 0 (Wave D), so the
	// next failure climbs to level 1 again rather than jumping to level 2.
	assert.True(t, IsRouteHealthy(key, now.Add(3*time.Second)))

	require.NoError(t, RecordRetryableFailure(key, "bad_response_status_code", FailureSourceUpstream, now.Add(3*time.Second)))
	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&row).Error)
	assert.Equal(t, 1, row.IsolationLevel,
		"a recovered route restarts the ladder instead of resuming from the residual level")
	assert.Equal(t, HealthCalm, row.State)
	require.NotNil(t, row.Until)
	assert.Equal(t, now.Add(3*time.Second).Unix()+3, *row.Until, "level 1 lasts 3s")
}

// TestExpiredIsolationDecaysAndPersistsHealthy verifies that selector-side lazy
// recovery uses the versioned DB update, rather than merely treating an expired
// cache entry as healthy forever, and that the elapsed window also pays down the
// ladder by one decay step (Wave D). Without the decay, the next failure would
// resume from the residual level and skip the whole fast band.
func TestExpiredIsolationDecaysAndPersistsHealthy(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	until := now.Add(-time.Second).Unix()
	key := RouteKey{ChannelId: 9051, Model: "expired-route"}
	require.NoError(t, DB.Create(&ChannelModelHealth{
		ChannelId: key.ChannelId, Model: key.Model, State: HealthCalm,
		IsolationLevel: 2, Until: &until, Version: 4,
	}).Error)
	InitChannelModelHealthCache()

	require.True(t, IsRouteHealthy(key, now))

	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&row).Error)
	assert.Equal(t, HealthHealthy, row.State)
	assert.Nil(t, row.Until)
	assert.Equal(t, 5, row.Version)
	assert.Equal(t, 1, row.IsolationLevel,
		"an elapsed window pays down one decay step at normal pressure")
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

		require.NoError(t, RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, now))

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

		require.NoError(t, RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, now))

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

	require.NoError(t, RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, now))
	require.NoError(t, RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, now))
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
	for i := range 4 {
		require.NoError(t, RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, now.Add(time.Duration(i)*time.Hour)))
		var row ChannelModelHealth
		require.NoError(t, DB.Where("channel_id = ?", key.ChannelId).First(&row).Error)
		assert.False(t, seen[row.Version], "version %d reused", row.Version)
		seen[row.Version] = true
	}
}

// TestConcurrentRetryableFailureCASContention verifies that when multiple
// goroutines concurrently call RecordRetryableFailure for the same RouteKey,
// every accepted CAS write produces a unique version and no failure is
// silently lost. At least N-1 calls must succeed in bumping the level; the
// CAS retry loop absorbs the loser(s) without corrupting state.
func TestConcurrentRetryableFailureCASContention(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9401, Model: "concurrent-model"}

	// Pre-create the row so all 10 goroutines compete on the CAS update path,
	// not on INSERT. This mirrors the real multi-instance scenario where the
	// row already exists and two processes race to bump the version.
	require.NoError(t, DB.Create(&ChannelModelHealth{
		ChannelId: key.ChannelId, Model: key.Model, State: HealthHealthy, Version: 1,
	}).Error)
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, now); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		assert.NoError(t, err, "CAS retry should absorb contention without error")
	}

	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&row).Error)
	assert.Equal(t, 10, row.IsolationLevel, "every concurrent failure must escalate exactly once")
	assert.GreaterOrEqual(t, row.Version, 11, "version must be at least initial+10 after 10 accepted writes")
	assert.NotEqual(t, HealthHealthy, row.State, "the route must be isolated after 10 failures")
}

// TestFullLadderEscalation drives the relay path from level 1 through level 10,
// verifying the state transitions calm→calm→dormant and the corresponding
// durations at each step match the configured ladder. This covers #375's
// requirement for a complete level 4–10 simulation through RecordRetryableFailure.
//
// The auto-disable threshold is pinned to 0 here so the ladder runs to its
// ceiling: with the default threshold of 3, the dormant levels would trip the
// circuit breaker partway through. That path has its own coverage in
// TestDormantMultiCycleThresholdAndSibling.
func TestFullLadderEscalation(t *testing.T) {
	withRouteHealthDB(t)
	cfg := operation_setting.DefaultChannelModelHealthSetting()
	cfg.DormantDisableThreshold = 0
	withHealthSetting(t, cfg)

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9501, Model: "ladder-model"}

	for level := 1; level <= 10; level++ {
		require.NoError(t, RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, now.Add(time.Duration(level)*time.Hour)))

		var row ChannelModelHealth
		require.NoError(t, DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&row).Error)
		assert.Equal(t, level, row.IsolationLevel, "level %d", level)

		expectedState, expectedSeconds := isolationDuration(level, operation_setting.DefaultChannelModelHealthSetting())
		assert.Equal(t, expectedState, row.State, "level %d state", level)
		require.NotNil(t, row.Until, "level %d must have a deadline", level)
		assert.Equal(t, now.Add(time.Duration(level)*time.Hour).Unix()+expectedSeconds, *row.Until, "level %d until", level)
	}

	// Level 10+ stays dormant with DormantMaxBase.
	require.NoError(t, RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, now.Add(11*time.Hour)))
	var finalRow ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&finalRow).Error)
	assert.Equal(t, 11, finalRow.IsolationLevel)
	assert.Equal(t, HealthDormant, finalRow.State)
	_, maxSeconds := isolationDuration(11, operation_setting.DefaultChannelModelHealthSetting())
	require.NotNil(t, finalRow.Until)
	assert.Equal(t, now.Add(11*time.Hour).Unix()+maxSeconds, *finalRow.Until)
}

// TestDormantMultiCycleThresholdAndSibling covers #375's requirement that a
// positive threshold disables only after multiple dormant cycles, threshold 0
// never disables, and a sibling model on the same channel remains unaffected
// throughout. This is a superset of TestDormantExpiryDisableThreshold: it cycles
// dormant→expiry→failure twice and checks the sibling at each step.
func TestDormantMultiCycleThresholdAndSibling(t *testing.T) {
	t.Run("threshold 3 disables on third dormant expiry", func(t *testing.T) {
		withRouteHealthDB(t)
		cfg := operation_setting.DefaultChannelModelHealthSetting()
		cfg.DormantDisableThreshold = 3
		withHealthSetting(t, cfg)

		now := time.Unix(1_700_000_000, 0)
		key := RouteKey{ChannelId: 9601, Model: "dormant-cycler"}
		sibling := RouteKey{ChannelId: 9601, Model: "dormant-sibling"}

		// Drive to level 9 (dormant) with an expired deadline.
		driveToExpiredDormant := func(base time.Time) {
			t.Helper()
			// Levels 1–9: 9 consecutive failures at different times to avoid
			// triggering the dormant-expiry counter mid-escalation.
			for i := range 9 {
				require.NoError(t, RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, base.Add(time.Duration(i)*time.Second)))
			}
			var row ChannelModelHealth
			require.NoError(t, DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&row).Error)
			assert.Equal(t, HealthDormant, row.State)
			// Expire the dormant window.
			require.NotNil(t, row.Until)
			assert.True(t, *row.Until < base.Unix()+int64(cfg.DormantBase+8*cfg.DormantInterval)+1)
		}

		// Cycle 1: dormant expired, fail again → count=1, still dormant.
		driveToExpiredDormant(now)
		require.NoError(t, RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, now.Add(time.Hour)))
		var row1 ChannelModelHealth
		require.NoError(t, DB.Where("channel_id = ?", key.ChannelId).First(&row1).Error)
		assert.Equal(t, 1, row1.DormantDisableCount, "first expired-dormant failure")
		assert.NotEqual(t, HealthDisabled, row1.State, "threshold 3 not yet reached")

		// Sibling must be healthy throughout.
		assert.True(t, IsRouteHealthy(sibling, now), "sibling unaffected by dormant cycling")

		// Cycle 2: expire and fail → count=2, still not disabled.
		require.NoError(t, RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, now.Add(2*time.Hour)))
		var row2 ChannelModelHealth
		require.NoError(t, DB.Where("channel_id = ?", key.ChannelId).First(&row2).Error)
		assert.Equal(t, 2, row2.DormantDisableCount)
		assert.NotEqual(t, HealthDisabled, row2.State, "threshold 3 not yet reached")
		assert.True(t, IsRouteHealthy(sibling, now), "sibling still healthy after cycle 2")

		// Cycle 3: expire and fail → count=3, disabled.
		require.NoError(t, RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, now.Add(3*time.Hour)))
		var row3 ChannelModelHealth
		require.NoError(t, DB.Where("channel_id = ?", key.ChannelId).First(&row3).Error)
		assert.Equal(t, 3, row3.DormantDisableCount)
		assert.Equal(t, HealthDisabled, row3.State, "threshold 3 reached → disabled")
		assert.Nil(t, row3.Until, "disabled has no expiry")
		assert.False(t, IsRouteHealthy(key, now.Add(365*24*time.Hour)), "disabled never self-heals")
		assert.True(t, IsRouteHealthy(sibling, now), "sibling unaffected even after disable")
	})

	t.Run("threshold 0 cycles forever without disabling", func(t *testing.T) {
		withRouteHealthDB(t)
		cfg := operation_setting.DefaultChannelModelHealthSetting()
		cfg.DormantDisableThreshold = 0
		withHealthSetting(t, cfg)

		now := time.Unix(1_700_000_000, 0)
		key := RouteKey{ChannelId: 9602, Model: "cycler-zero"}
		sibling := RouteKey{ChannelId: 9602, Model: "sibling-zero"}
		prevCount := 0

		for cycle := 0; cycle < 5; cycle++ {
			// Each cycle uses a base time well past the previous cycle's dormant
			// deadline so the expiry counter fires. The dormant window at level 9
			// is DormantBase+8*DormantInterval = 120+640 = 760s with defaults, so
			// advance by 10000s per cycle to guarantee expiry.
			cycleBase := now.Add(time.Duration(cycle*10000) * time.Second)
			// Drive to dormant (levels 1-9).
			for i := range 9 {
				require.NoError(t, RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, cycleBase.Add(time.Duration(i)*time.Second)))
			}
			// Fail after the dormant window has expired.
			require.NoError(t, RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, cycleBase.Add(2000*time.Second)))
			var row ChannelModelHealth
			require.NoError(t, DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&row).Error)
			assert.Greater(t, row.DormantDisableCount, prevCount, "cycle %d: counter must keep climbing", cycle)
			prevCount = row.DormantDisableCount
			assert.NotEqual(t, HealthDisabled, row.State, "threshold 0 never disables")
			assert.True(t, IsRouteHealthy(sibling, now), "sibling healthy in cycle %d", cycle)
		}
	})
}

// TestCacheHydrationAndExpiryPersistence covers #375's requirement that a
// restart rehydrates persisted isolation from DB, a selector expiry CAS writes
// healthy back to DB, and a subsequent re-hydration sees the healthy state.
func TestCacheHydrationAndExpiryPersistence(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9701, Model: "hydration-model"}

	// Persist a calm row with an already-expired deadline.
	expiredUntil := now.Add(-time.Minute).Unix()
	require.NoError(t, DB.Create(&ChannelModelHealth{
		ChannelId:      key.ChannelId,
		Model:          key.Model,
		State:          HealthCalm,
		IsolationLevel: 3,
		Until:          &expiredUntil,
		Version:        5,
	}).Error)

	// Simulate restart: clear cache and re-hydrate from DB.
	ClearRouteHealthCache()
	InitChannelModelHealthCache()

	// Selector sees the route as healthy because the deadline has passed.
	assert.True(t, IsRouteHealthy(key, now))

	// The CAS must have persisted healthy to the DB.
	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&row).Error)
	assert.Equal(t, HealthHealthy, row.State, "CAS must persist healthy after expiry")
	assert.Nil(t, row.Until)
	assert.Equal(t, 6, row.Version, "version must bump")
	assert.Equal(t, 2, row.IsolationLevel,
		"the elapsed window pays down one decay step while keeping the rest of the ladder")

	// Simulate a second restart: re-hydrate and verify the healthy state survives.
	ClearRouteHealthCache()
	InitChannelModelHealthCache()

	assert.True(t, IsRouteHealthy(key, now), "no spurious isolation after re-hydration")

	var row2 ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&row2).Error)
	assert.Equal(t, HealthHealthy, row2.State, "DB must still be healthy after second hydration")
	assert.Nil(t, row2.Until)
}

// TestGetRouteIsolationReportsTransition covers the snapshot the relay layer
// logs after a transition. Without a populated state/level/until an operator
// cannot tell a RouteKey isolation apart from a plain upstream failure, so the
// accessor must report the ladder position and a future deadline, and must
// report ok=false for a route that was never isolated.
func TestGetRouteIsolationReportsTransition(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	key := RouteKey{ChannelId: 9801, Model: "isolation-report-model"}
	untouched := RouteKey{ChannelId: 9801, Model: "never-failed-model"}
	now := time.Unix(1_800_000_000, 0)

	state, level, until, ok := GetRouteIsolation(untouched)
	assert.False(t, ok, "a route with no record has no isolation snapshot")
	assert.Equal(t, HealthHealthy, state)
	assert.Zero(t, level)
	assert.Zero(t, until)

	require.NoError(t, RecordRetryableFailure(key, "do_request_failed", FailureSourceUpstream, now))

	state, level, until, ok = GetRouteIsolation(key)
	require.True(t, ok, "an isolated route must expose its snapshot")
	assert.Equal(t, HealthCalm, state)
	assert.Equal(t, 1, level)
	cfg := operation_setting.DefaultChannelModelHealthSetting()
	assert.Equal(t, now.Unix()+int64(cfg.CalmFastBase), until, "deadline must match the level 1 duration")

	// Escalation must be visible through the same accessor, otherwise the log
	// would keep reporting a stale level.
	require.NoError(t, RecordRetryableFailure(key, "do_request_failed", FailureSourceUpstream, now))
	_, level, _, ok = GetRouteIsolation(key)
	require.True(t, ok)
	assert.Equal(t, 2, level, "the accessor must follow the ladder")
}

// TestConcurrentFailureNeverLosesUpdate pins the invariant behind #375's first
// acceptance item: no retry-eligible failure may be silently dropped. A fixed
// three-attempt CAS bound used to fail here, because with N writers a loser can
// lose N-1 races in a row and return an error while its failure vanishes from
// the ladder. The writer count exceeds the old bound on purpose.
func TestConcurrentFailureNeverLosesUpdate(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9901, Model: "no-lost-update-model"}
	const writers = 25

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, now); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		assert.NoError(t, err, "no writer may be rejected: a dropped failure under-counts the ladder")
	}

	var row ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&row).Error)
	assert.Equal(t, writers, row.IsolationLevel, "every concurrent failure must escalate the ladder exactly once")
	assert.Equal(t, writers+1, row.Version, "version must advance once per accepted write")
}

// TestLocalFailureThresholdEscalatesAtThreshold verifies that local failures
// are counted separately from upstream failures: with LocalFailureThreshold=3,
// two local failures stay at level 0 with LocalFailureCount=2, and only the
// third local failure resets the counter and escalates to level 1. Upstream
// counter must remain zero throughout.
func TestLocalFailureThresholdEscalatesAtThreshold(t *testing.T) {
	withRouteHealthDB(t)
	cfg := operation_setting.DefaultChannelModelHealthSetting()
	cfg.LocalFailureThreshold = 3
	withHealthSetting(t, cfg)

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9120, Model: "local-threshold"}

	// First local failure: count 1, still level 0, still healthy.
	require.NoError(t, RecordRetryableFailure(key, "no_available_channel", FailureSourceLocal, now))
	var row1 ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&row1).Error)
	assert.Equal(t, 1, row1.LocalFailureCount, "first local failure: count 1")
	assert.Equal(t, 0, row1.UpstreamFailureCount, "upstream counter untouched")
	assert.Equal(t, 0, row1.IsolationLevel, "below threshold: no escalation")
	assert.Equal(t, HealthHealthy, row1.State, "below threshold: still healthy")
	assert.True(t, IsRouteHealthy(key, now), "below threshold: route selectable")

	// Second local failure: count 2, still level 0, still healthy.
	require.NoError(t, RecordRetryableFailure(key, "no_available_channel", FailureSourceLocal, now.Add(time.Second)))
	var row2 ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&row2).Error)
	assert.Equal(t, 2, row2.LocalFailureCount, "second local failure: count 2")
	assert.Equal(t, 0, row2.UpstreamFailureCount, "upstream counter still zero")
	assert.Equal(t, 0, row2.IsolationLevel, "still below threshold: no escalation")
	assert.Equal(t, HealthHealthy, row2.State)

	// Third local failure: reaches threshold → reset to 0, escalate to level 1.
	require.NoError(t, RecordRetryableFailure(key, "no_available_channel", FailureSourceLocal, now.Add(2*time.Second)))
	var row3 ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&row3).Error)
	assert.Equal(t, 0, row3.LocalFailureCount, "threshold reached: local counter reset")
	assert.Equal(t, 0, row3.UpstreamFailureCount, "upstream counter still zero after local escalation")
	assert.Equal(t, 1, row3.IsolationLevel, "threshold reached: escalated to level 1")
	assert.Equal(t, HealthCalm, row3.State)
	assert.False(t, IsRouteHealthy(key, now.Add(2*time.Second)), "isolated after escalation")
}

// TestUpstreamFailureThresholdEscalatesWhilePreservingLocalCounter verifies
// that an upstream failure escalates immediately when UpstreamFailureThreshold=1
// while the local counter accumulated by prior local failures is preserved.
func TestUpstreamFailureThresholdEscalatesWhilePreservingLocalCounter(t *testing.T) {
	withRouteHealthDB(t)
	cfg := operation_setting.DefaultChannelModelHealthSetting()
	cfg.LocalFailureThreshold = 3
	cfg.UpstreamFailureThreshold = 1
	withHealthSetting(t, cfg)

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9121, Model: "upstream-preserve-local"}

	// Accumulate 2 local failures (below threshold 3).
	require.NoError(t, RecordRetryableFailure(key, "no_available_channel", FailureSourceLocal, now))
	require.NoError(t, RecordRetryableFailure(key, "no_available_channel", FailureSourceLocal, now.Add(time.Second)))
	var rowA ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&rowA).Error)
	assert.Equal(t, 2, rowA.LocalFailureCount, "two local failures counted")
	assert.Equal(t, 0, rowA.IsolationLevel, "local below threshold: no escalation")

	// One upstream failure: threshold 1 → escalate immediately.
	require.NoError(t, RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, now.Add(2*time.Second)))
	var rowB ChannelModelHealth
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&rowB).Error)
	assert.Equal(t, 0, rowB.UpstreamFailureCount, "upstream counter reset after escalation")
	assert.Equal(t, 2, rowB.LocalFailureCount, "local counter preserved across upstream escalation")
	assert.Equal(t, 1, rowB.IsolationLevel, "upstream threshold 1 escalates immediately")
	assert.Equal(t, HealthCalm, rowB.State)
	assert.False(t, IsRouteHealthy(key, now.Add(2*time.Second)))
}

// TestRecordRetryableFailureRejectsUnknownSource verifies that an invalid
// FailureSource is rejected before any DB mutation — no row is created, no
// cache entry is inserted, and the error is returned.
func TestRecordRetryableFailureRejectsUnknownSource(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9122, Model: "bad-source"}

	err := RecordRetryableFailure(key, "err", FailureSource("bogus"), now)
	require.Error(t, err, "unknown source must be rejected")
	assert.Contains(t, err.Error(), "unknown failure source")

	var count int64
	require.NoError(t, DB.Model(&ChannelModelHealth{}).Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).Count(&count).Error)
	assert.Zero(t, count, "no DB row should be created for an invalid source")
	assert.True(t, IsRouteHealthy(key, now), "no cache entry, route healthy by default")
}

func TestRouteHealthSeparatesKeysForSameChannelModel(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	now := time.Unix(1_700_000_000, 0)
	failed := RouteKey{ChannelId: 9123, KeyIndex: 0, Model: "key-separated"}
	healthy := RouteKey{ChannelId: 9123, KeyIndex: 1, Model: "key-separated"}

	require.NoError(t, RecordRetryableFailure(failed, "bad_response", FailureSourceUpstream, now))
	assert.False(t, IsRouteHealthy(failed, now))
	assert.True(t, IsRouteHealthy(healthy, now), "one failed key must not isolate a healthy sibling key")

	var count int64
	require.NoError(t, DB.Model(&ChannelModelHealth{}).Where("channel_id = ? AND model = ?", failed.ChannelId, failed.Model).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// TestRouteWeightMultiplierCalmAndDormant verifies the Wave C soft deprecation
// contract: calm and dormant routes remain selectable (IsRouteSelectable=true)
// but receive a weight multiplier below 1.0, while disabled routes are fully
// excluded (IsRouteSelectable=false, multiplier=0). A healthy route gets 1.0.
func TestRouteWeightMultiplierCalmAndDormant(t *testing.T) {
	withRouteHealthDB(t)
	cfg := operation_setting.DefaultChannelModelHealthSetting()
	cfg.CalmWeightScale = 50
	cfg.DormantWeightScale = 10
	withHealthSetting(t, cfg)

	now := time.Unix(1_700_000_000, 0)
	healthy := RouteKey{ChannelId: 9130, KeyIndex: 0, Model: "wm-healthy"}
	calm := RouteKey{ChannelId: 9131, KeyIndex: 0, Model: "wm-calm"}
	dormant := RouteKey{ChannelId: 9132, KeyIndex: 0, Model: "wm-dormant"}
	disabled := RouteKey{ChannelId: 9133, KeyIndex: 0, Model: "wm-disabled"}

	// healthy: no isolation row
	assert.Equal(t, 1.0, RouteWeightMultiplier(healthy))
	assert.True(t, IsRouteSelectable(healthy))

	// calm: escalate one level, then check multiplier
	require.NoError(t, RecordRetryableFailure(calm, "bad_response", FailureSourceUpstream, now))
	assert.InDelta(t, 0.5, RouteWeightMultiplier(calm), 1e-9)
	assert.True(t, IsRouteSelectable(calm), "calm routes remain selectable")

	// dormant: escalate to level 7+ (dormant range)
	for i := range 7 {
		require.NoError(t, RecordRetryableFailure(dormant, "bad_response", FailureSourceUpstream, now.Add(time.Duration(i)*time.Hour)))
	}
	assert.InDelta(t, 0.1, RouteWeightMultiplier(dormant), 1e-9)
	assert.True(t, IsRouteSelectable(dormant), "dormant routes remain selectable")

	// disabled: seed a disabled row directly
	require.NoError(t, DisableRoute(disabled, now))
	assert.Equal(t, 0.0, RouteWeightMultiplier(disabled))
	assert.False(t, IsRouteSelectable(disabled), "disabled routes are excluded")
}

// TestSoftDepressionKeepsCalmRouteSelectable verifies the end-to-end selection
// behavior: a calm route with reduced weight must still appear as a candidate,
// unlike a disabled route which is dropped. With CalmWeightScale=100 the calm
// route competes at full weight (no degradation), and with CalmWeightScale=0
// the calm route's weight becomes zero (effectively excluded without being
// disabled).
func TestSoftDepressionKeepsCalmRouteSelectable(t *testing.T) {
	withRouteHealthDB(t)
	cfg := operation_setting.DefaultChannelModelHealthSetting()
	cfg.CalmWeightScale = 100
	withHealthSetting(t, cfg)

	now := time.Unix(1_700_000_000, 0)
	key := RouteKey{ChannelId: 9140, KeyIndex: 0, Model: "soft-calm"}

	// Escalate to calm (level 1).
	require.NoError(t, RecordRetryableFailure(key, "bad_response", FailureSourceUpstream, now))

	// With CalmWeightScale=100 the calm route competes at full weight.
	assert.InDelta(t, 1.0, RouteWeightMultiplier(key), 1e-9)
	assert.True(t, IsRouteSelectable(key), "calm route is still a candidate")
}
