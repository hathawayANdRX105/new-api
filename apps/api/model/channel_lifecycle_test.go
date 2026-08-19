package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// restoreDefaultResetHook reinstalls the production hook after a test swaps it,
// so later tests still observe kill-switch cleanup.
func restoreDefaultResetHook(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		operation_setting.RegisterHealthStateResetHook(func() {
			GetChannelHealthManager().Reset()
		})
	})
}

// TestKillSwitchDisableClearsHealthState: Reset existed but nothing called it, so
// disabling and re-enabling the kill switch resurrected the old scores and made
// the toggle useless as a recovery lever.
func TestKillSwitchDisableClearsHealthState(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)

	const channelID = 8200
	for range 60 {
		mgr.RecordChannelOutcome(channelID, OutcomeFatal)
	}
	require.InDelta(t, 0.05, mgr.GetScore(channelID), 1e-9, "channel is degraded to the floor")

	setTestConfig(false, 0.3, 0.05, 0)
	setTestConfig(true, 0.3, 0.05, 0)

	assert.Equal(t, 1.0, mgr.GetScore(channelID),
		"re-enabling must start from a clean slate, not the pre-disable score")
}

// TestKillSwitchEnabledToEnabledKeepsState: only the disable edge clears state.
// Tuning alpha or min_score while the switch stays on must not wipe history.
func TestKillSwitchEnabledToEnabledKeepsState(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)

	const channelID = 8201
	for range 20 {
		mgr.RecordChannelOutcome(channelID, OutcomeFatal)
	}
	degraded := mgr.GetScore(channelID)
	require.Less(t, degraded, 0.2)

	setTestConfig(true, 0.6, 0.05, 0) // still enabled, different alpha

	assert.InDelta(t, degraded, mgr.GetScore(channelID), 1e-9,
		"an enabled -> enabled config change must preserve accumulated health")
}

// TestKillSwitchResetHookFiresOnlyOnDisableEdge pins the trigger condition
// directly, which the score-based tests cannot distinguish on their own.
func TestKillSwitchResetHookFiresOnlyOnDisableEdge(t *testing.T) {
	restoreDefaultResetHook(t)

	calls := 0
	operation_setting.RegisterHealthStateResetHook(func() { calls++ })

	setTestConfig(true, 0.3, 0.05, 0) // ensure we start enabled
	require.Equal(t, 0, calls, "enabling must not trigger cleanup")

	setTestConfig(false, 0.3, 0.05, 0)
	assert.Equal(t, 1, calls, "the enabled -> disabled edge triggers cleanup once")

	setTestConfig(false, 0.3, 0.05, 0)
	assert.Equal(t, 1, calls, "disabled -> disabled must not re-trigger")

	setTestConfig(true, 0.3, 0.05, 0)
	assert.Equal(t, 1, calls, "disabled -> enabled must not trigger")

	setTestConfig(false, 0.3, 0.05, 0)
	assert.Equal(t, 2, calls, "a later disable edge triggers again")
}

// TestSlowStartRampsWeightLinearly pins the warm-up curve. Previously a channel
// inside the MinRequests window competed at full weight, so a channel failing
// every request still won its first MinRequests picks outright.
//
// The first pick is deliberately full weight: a channel with no observed outcome
// is indistinguishable from one with no state at all, and EffectiveWeight
// short-circuits that case (three pre-existing tests assert it). The ramp
// therefore governs picks 2..MinRequests, scaling by requestCount/MinRequests.
func TestSlowStartRampsWeightLinearly(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 5)

	const channelID = 8202
	const baseWeight = 10

	expected := []float64{10, 2, 4, 6, 8, 10, 10}
	for i, want := range expected {
		got := mgr.EffectiveWeight(channelID, baseWeight)
		assert.InDelta(t, want, got, 1e-9,
			"pick %d: ramp scales by observed requestCount/MinRequests", i+1)
		mgr.RecordChannelOutcome(channelID, OutcomeSuccess)
	}
}

// TestSlowStartExitsOnFirstFatal: a channel that fails immediately must not keep
// climbing the ramp. This is the AWS ALB rule that a target leaves slow start as
// soon as it becomes unhealthy.
func TestSlowStartExitsOnFirstFatal(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 5)

	const channelID = 8203
	mgr.RecordChannelOutcome(channelID, OutcomeFatal)

	// requestCount is 1 of 5, so an unbroken ramp would still apply 0.4 here.
	// Having exited, the only remaining derate is the health score itself, which
	// is untouched inside the MinRequests guard.
	assert.InDelta(t, 10.0, mgr.EffectiveWeight(channelID, 10), 1e-9,
		"the ramp is abandoned, leaving health scoring as the only derate")
}

// TestSlowStartDeniesBrokenChannelFreeFullWeight is the user-visible consequence:
// during warm-up a failing channel must not tie a succeeding one.
func TestSlowStartDeniesBrokenChannelFreeFullWeight(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 5)

	const broken, healthy = 8204, 8205
	mgr.RecordChannelOutcome(broken, OutcomeNeutral)
	mgr.RecordChannelOutcome(healthy, OutcomeNeutral)

	// Both are at requestCount 0, hence identical ramp factors.
	require.InDelta(t, mgr.EffectiveWeight(broken, 10), mgr.EffectiveWeight(healthy, 10), 1e-9)

	for range 6 {
		mgr.RecordChannelOutcome(broken, OutcomeFatal)
		mgr.RecordChannelOutcome(healthy, OutcomeSuccess)
	}

	assert.Less(t, mgr.EffectiveWeight(broken, 10), mgr.EffectiveWeight(healthy, 10),
		"the failing channel must end up strictly below the healthy one")
}

// TestSlowStartDisabledWhenMinRequestsZero: with no warm-up window configured the
// ramp must be inert, preserving the previous behaviour exactly.
func TestSlowStartDisabledWhenMinRequestsZero(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)

	const channelID = 8206
	mgr.RecordChannelOutcome(channelID, OutcomeSuccess)

	assert.InDelta(t, 10.0, mgr.EffectiveWeight(channelID, 10), 1e-9,
		"MinRequests=0 disables the ramp entirely")
}

// TestSlowStartIgnoredWhenKillSwitchOff: the switch must remain a complete bypass,
// so neither health scoring nor the ramp may alter the configured weight.
func TestSlowStartIgnoredWhenKillSwitchOff(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 5)

	const channelID = 8207
	// Seed with a scored outcome: OutcomeNeutral deliberately leaves requestCount
	// at zero, which the ramp treats as "not yet observed" and does not derate.
	mgr.RecordChannelOutcome(channelID, OutcomeSuccess)
	require.InDelta(t, 2.0, mgr.EffectiveWeight(channelID, 10), 1e-9, "ramp applies while enabled")

	setTestConfig(false, 0.3, 0.05, 5)
	assert.InDelta(t, 10.0, mgr.EffectiveWeight(channelID, 10), 1e-9,
		"with the kill switch off the configured weight passes through untouched")
}

// TestSlowStartRampSurvivesReset: Reset rebuilds the state map, so a channel that
// had exited the ramp starts warming up again.
func TestSlowStartRampSurvivesReset(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 5)

	const channelID = 8208
	mgr.RecordChannelOutcome(channelID, OutcomeFatal)
	require.InDelta(t, 10.0, mgr.EffectiveWeight(channelID, 10), 1e-9, "ramp exited")

	mgr.Reset()
	mgr.RecordChannelOutcome(channelID, OutcomeSuccess)

	assert.InDelta(t, 2.0, mgr.EffectiveWeight(channelID, 10), 1e-9,
		"after Reset the channel warms up from the start again")
}
