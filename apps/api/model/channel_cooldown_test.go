package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func configureCooldownTest(t *testing.T, cfg *operation_setting.ChannelHealthSetting, now *time.Time) {
	t.Helper()

	previous := *operation_setting.GetChannelHealthSetting()
	previousNow := channelHealthNow
	operation_setting.SetChannelHealthSetting(cfg)
	if now != nil {
		channelHealthNow = func() time.Time { return *now }
	}
	t.Cleanup(func() {
		channelHealthNow = previousNow
		operation_setting.SetChannelHealthSetting(&previous)
	})
}

func cooldownTestSetting() *operation_setting.ChannelHealthSetting {
	return operation_setting.DefaultChannelHealthSetting()
}

func TestChannelHealthCooldownConfigNormalizesAndPreservesInput(t *testing.T) {
	cfg := cooldownTestSetting()
	cfg.CooldownThreshold = 0
	cfg.CooldownBaseSeconds = -1
	cfg.CooldownMaxSeconds = -2
	cfg.CooldownMaxEjectionPercent = 101
	cfg.CooldownAlpha = -1
	configureCooldownTest(t, cfg, nil)

	got := operation_setting.GetChannelHealthSetting()
	require.NotNil(t, got)
	assert.Equal(t, 1, got.CooldownThreshold)
	assert.Equal(t, 0, got.CooldownBaseSeconds)
	assert.Equal(t, 0, got.CooldownMaxSeconds)
	assert.Equal(t, 100, got.CooldownMaxEjectionPercent)
	assert.Equal(t, 0.0, got.CooldownAlpha)

	assert.Equal(t, 0, cfg.CooldownThreshold, "publication must not mutate the caller's struct")
	assert.Equal(t, -1, cfg.CooldownBaseSeconds)
	assert.Equal(t, -2, cfg.CooldownMaxSeconds)
	assert.Equal(t, 101, cfg.CooldownMaxEjectionPercent)
	assert.Equal(t, -1.0, cfg.CooldownAlpha)
}

func TestChannelHealthCooldownOptionUpdateRejectsInvalidValue(t *testing.T) {
	configureCooldownTest(t, cooldownTestSetting(), nil)

	require.NoError(t, operation_setting.UpdateChannelHealthSettingValue(
		"ChannelHealthCooldownThreshold", "7",
	))
	assert.Equal(t, 7, operation_setting.GetChannelHealthSetting().CooldownThreshold)

	before := *operation_setting.GetChannelHealthSetting()
	assert.Error(t, operation_setting.UpdateChannelHealthSettingValue(
		"ChannelHealthCooldownAlpha", "not-a-number",
	))
	assert.Error(t, operation_setting.UpdateChannelHealthSettingValue("unknown", "1"))
	assert.Equal(t, before, *operation_setting.GetChannelHealthSetting())

	// Out-of-range values are rejected rather than silently clamped: a clamped
	// value would leave the persisted option and the effective config disagreeing.
	for key, value := range map[string]string{
		"ChannelHealthCooldownThreshold":          "0",
		"ChannelHealthCooldownBaseSeconds":        "-1",
		"ChannelHealthCooldownMaxSeconds":         "-1",
		"ChannelHealthCooldownMaxEjectionPercent": "101",
		"ChannelHealthCooldownAlpha":              "1.5",
	} {
		assert.Error(t, operation_setting.UpdateChannelHealthSettingValue(key, value), key)
		assert.Error(t, validateOptionValue(key, value), key)
	}
	assert.Equal(t, before, *operation_setting.GetChannelHealthSetting(),
		"a rejected value must not change the live config")

	// An inverted base/max pair is rejected too: normalization would raise max up
	// to base, so accepting it would store a duration that never takes effect.
	assert.Error(t, operation_setting.UpdateChannelHealthSettingValue(
		"ChannelHealthCooldownMaxSeconds", "5",
	), "max below the configured base must be rejected")
	require.NoError(t, operation_setting.UpdateChannelHealthSettingValue(
		"ChannelHealthCooldownMaxSeconds", "90",
	))
	assert.Equal(t, 90, operation_setting.GetChannelHealthSetting().CooldownMaxSeconds)
}

func TestChannelCooldownTriggersOnConsecutiveThrottleAndFatal(t *testing.T) {
	mgr := resetHealthManager()
	now := time.Unix(1_700_000_000, 0)
	cfg := cooldownTestSetting()
	cfg.MinRequests = 0
	cfg.CooldownThreshold = 2
	cfg.CooldownMaxEjectionPercent = 100
	configureCooldownTest(t, cfg, &now)

	const channelID = 9901
	throttle := ClassifyChannelOutcome(upstreamError("rate_limit_exceeded", 429), channelID)
	require.Equal(t, OutcomeThrottled, throttle)
	mgr.RecordChannelOutcome(channelID, throttle)
	assert.Greater(t, mgr.EffectiveWeight(channelID, 10), 0.0)

	fatal := ClassifyChannelOutcome(upstreamError("upstream_error", 503), channelID)
	require.Equal(t, OutcomeFatal, fatal)
	mgr.RecordChannelOutcome(channelID, fatal)
	assert.Zero(t, mgr.EffectiveWeight(channelID, 10), "the threshold outcome must eject the channel")

	mgr.Reset()
	mgr.RecordChannelOutcome(channelID, OutcomeThrottled)
	mgr.RecordChannelOutcome(channelID, OutcomeNeutral)
	mgr.RecordChannelOutcome(channelID, OutcomeFatal)
	assert.Greater(t, mgr.EffectiveWeight(channelID, 10), 0.0, "neutral breaks the consecutive failure run")
}

func TestChannelCooldownDurationSlidesFromBaseTowardMaximum(t *testing.T) {
	cfg := cooldownTestSetting()
	cfg.CooldownBaseSeconds = 30
	cfg.CooldownMaxSeconds = 60
	cfg.CooldownAlpha = 0.3

	assert.Equal(t, 30*time.Second, cooldownDuration(cfg, 0))
	assert.InDelta(t, float64(51*time.Second), float64(cooldownDuration(cfg, 1)), 1)
	assert.InDelta(t, 57.3*float64(time.Second), float64(cooldownDuration(cfg, 2)), 1)
	assert.LessOrEqual(t, cooldownDuration(cfg, 100), 60*time.Second)
}

func TestChannelCooldownExpiryReentersSlowStartAndDecaysStreak(t *testing.T) {
	mgr := resetHealthManager()
	now := time.Unix(1_700_000_000, 0)
	cfg := cooldownTestSetting()
	cfg.MinRequests = 5
	cfg.CooldownThreshold = 1
	cfg.CooldownMaxEjectionPercent = 100
	configureCooldownTest(t, cfg, &now)

	const channelID = 9902
	mgr.RecordChannelOutcome(channelID, OutcomeFatal)
	require.Zero(t, mgr.EffectiveWeight(channelID, 10))

	now = now.Add(31 * time.Second)
	assert.InDelta(t, 2.0, mgr.EffectiveWeight(channelID, 10), 1e-9)

	mgr.mu.Lock()
	state := mgr.states[channelID]
	require.NotNil(t, state)
	assert.Zero(t, state.requestCount)
	assert.True(t, state.rampPending)
	assert.False(t, state.rampExited)
	assert.Equal(t, 1, state.cooldownStreak)
	mgr.mu.Unlock()

	mgr.RecordChannelOutcome(channelID, OutcomeSuccess)
	// That success clears rampPending and sets requestCount=1. requestCount(1)
	// <= MinRequests(5) so the score stays 1.0, and the ramp factor is 1/5.
	assert.InDelta(t, 2.0, mgr.EffectiveWeight(channelID, 10), 1e-9)

	mgr.mu.Lock()
	assert.Zero(t, state.cooldownStreak, "a clean recovered outcome decays the cooldown streak")
	mgr.mu.Unlock()
}

func TestChannelCooldownKillSwitchAndLegacyRecordOutcome(t *testing.T) {
	mgr := resetHealthManager()
	now := time.Unix(1_700_000_000, 0)
	cfg := cooldownTestSetting()
	cfg.MinRequests = 0
	cfg.CooldownThreshold = 1
	configureCooldownTest(t, cfg, &now)

	const channelID = 9903
	mgr.RecordOutcome(channelID, false)
	assert.Greater(t, mgr.EffectiveWeight(channelID, 10), 0.0, "legacy bool API must not start cooldown")

	mgr.RecordChannelOutcome(channelID, OutcomeFatal)
	require.Zero(t, mgr.EffectiveWeight(channelID, 10))

	cfg.Enabled = false
	operation_setting.SetChannelHealthSetting(cfg)
	assert.InDelta(t, 10.0, mgr.EffectiveWeight(channelID, 10), 1e-9)
}

func TestFilterCoolingChannelsHonorsEjectionCap(t *testing.T) {
	mgr := resetHealthManager()
	now := time.Unix(1_700_000_000, 0)
	cfg := cooldownTestSetting()
	cfg.CooldownThreshold = 1
	cfg.CooldownMaxEjectionPercent = 50
	configureCooldownTest(t, cfg, &now)

	mgr.RecordChannelOutcome(9904, OutcomeFatal)
	mgr.RecordChannelOutcome(9905, OutcomeFatal)

	// Both candidates are cooling, so the tier is fully cooling: the cap does not
	// apply and the whole tier is ejected so selection fails fast.
	assert.Equal(t, map[int]bool{9904: true, 9905: true},
		mgr.FilterCoolingChannels([]int{9905, 9904}, 50))
	assert.Equal(t, map[int]bool{9904: true, 9905: true},
		mgr.FilterCoolingChannels([]int{9904, 9905}, 100))
	assert.Empty(t, mgr.FilterCoolingChannels([]int{9904, 9905}, 0),
		"zero percent disables cooldown ejection")

	// A partially cooling tier honours the cap: 1 of 2 candidates cooling at 50%
	// ejects exactly that one, and the healthy peer is untouched.
	mgr.Reset()
	mgr.RecordChannelOutcome(9904, OutcomeFatal)
	assert.Equal(t, map[int]bool{9904: true},
		mgr.FilterCoolingChannels([]int{9905, 9904}, 50))
}

func TestChannelCooldownSelectionSkipsEjectedTierInMemoryAndDB(t *testing.T) {
	const group, modelName = "cooldown-group", "cooldown-model"
	now := time.Unix(1_700_000_000, 0)
	cfg := cooldownTestSetting()
	cfg.MinRequests = 0
	cfg.CooldownThreshold = 1
	cfg.CooldownMaxEjectionPercent = 100
	configureCooldownTest(t, cfg, &now)

	cooled := testChannel(9906, 10, 100)
	fallback := testChannel(9907, 10, 10)
	withChannelCacheFixture(t, []*Channel{cooled, fallback}, group, modelName)
	mgr := resetHealthManager()
	mgr.RecordChannelOutcome(cooled.Id, OutcomeFatal)

	got, err := GetRandomSatisfiedChannel(group, modelName, 0, "", nil)
	require.NoError(t, err)
	assert.Nil(t, got, "all candidates in the selected tier are ejected")
	got, err = GetRandomSatisfiedChannel(group, modelName, 1, "", nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, fallback.Id, got.Id)

	withAbilityDB(t, group, modelName, []Ability{
		ability(cooled.Id, group, modelName, 10, 100),
		ability(fallback.Id, group, modelName, 10, 10),
	})
	got, err = GetChannel(group, modelName, 0, "", nil)
	require.NoError(t, err)
	assert.Nil(t, got)
	got, err = GetChannel(group, modelName, 1, "", nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, fallback.Id, got.Id)
}

func TestChannelCooldownDurationSlidesFromBaseTenTowardMaximum(t *testing.T) {
	cfg := cooldownTestSetting()
	cfg.CooldownBaseSeconds = 10
	cfg.CooldownMaxSeconds = 60
	cfg.CooldownAlpha = 0.3

	// n=0: exactly base
	assert.Equal(t, 10*time.Second, cooldownDuration(cfg, 0))
	// n=1: 10 + 50*(1-0.3) = 45.0
	assert.InDelta(t, 45.0*float64(time.Second), float64(cooldownDuration(cfg, 1)), 1)
	// n=2: 10 + 50*(1-0.3^2) = 10 + 50*(1-0.09) = 55.5
	assert.InDelta(t, 55.5*float64(time.Second), float64(cooldownDuration(cfg, 2)), 1)
	// large n approaches but never exceeds max
	assert.LessOrEqual(t, cooldownDuration(cfg, 100), 60*time.Second)
}
