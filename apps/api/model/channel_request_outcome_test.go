package model

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// disableCall records one escalation so tests can assert attribution without
// touching the database.
type disableCall struct {
	channelID int
	modelName string
}

// withCapturedModelDisabler swaps the disable hook for a recorder and restores it
// afterwards. Escalation performs its write in a goroutine, so reads go through
// the mutex and callers poll rather than assuming immediate delivery.
func withCapturedModelDisabler(t *testing.T) func() []disableCall {
	t.Helper()
	var mu sync.Mutex
	var calls []disableCall
	previous := channelModelDisabler
	channelModelDisabler = func(channelID int, modelName string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, disableCall{channelID: channelID, modelName: modelName})
		return nil
	}
	t.Cleanup(func() { channelModelDisabler = previous })
	return func() []disableCall {
		mu.Lock()
		defer mu.Unlock()
		out := make([]disableCall, len(calls))
		copy(out, calls)
		return out
	}
}

// awaitDisableCalls waits briefly for the asynchronous escalation write.
func awaitDisableCalls(t *testing.T, read func() []disableCall, want int) []disableCall {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := read()
		if len(got) >= want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRecordRequestAttemptsIgnoresRecoveredFailures is the point of deferring
// health accounting. A 429 that a retry recovered from cost the caller nothing,
// so it must not push the first channel toward cooldown. Previously each failed
// try was charged the moment it happened, so a merely busy channel accumulated a
// failure streak even when every request ultimately succeeded elsewhere.
func TestRecordRequestAttemptsIgnoresRecoveredFailures(t *testing.T) {
	mgr := resetHealthManager()
	now := time.Unix(1_700_000_000, 0)
	cfg := cooldownTestSetting()
	cfg.MinRequests = 0
	cfg.CooldownThreshold = 2
	configureCooldownTest(t, cfg, &now)

	const throttled, winner = 9601, 9602

	// Ten requests that each hit a 429 and were then served by another channel.
	for range 10 {
		mgr.RecordRequestAttempts([]ChannelAttempt{
			{ChannelID: throttled, ModelName: "m", Outcome: OutcomeThrottled},
		}, winner, true)
	}

	assert.Greater(t, mgr.EffectiveWeight(throttled, 10), 0.0,
		"a channel whose failures were all recovered must not be ejected")
	assert.Equal(t, 1.0, mgr.GetScore(throttled),
		"and its score stays untouched, because no request actually failed")
}

// TestRecordRequestAttemptsChargesExhaustedRequests is the contrast case: once
// the retries are gone the request really failed, so every attempt it made counts
// and the streaks advance just as they would have without the deferral.
func TestRecordRequestAttemptsChargesExhaustedRequests(t *testing.T) {
	mgr := resetHealthManager()
	now := time.Unix(1_700_000_000, 0)
	cfg := cooldownTestSetting()
	cfg.MinRequests = 0
	cfg.CooldownThreshold = 2
	cfg.CooldownMaxEjectionPercent = 100
	configureCooldownTest(t, cfg, &now)

	const first, second = 9603, 9604

	// First exhausted request: one charged failure each, below the threshold of 2.
	mgr.RecordRequestAttempts([]ChannelAttempt{
		{ChannelID: first, ModelName: "m", Outcome: OutcomeFatal},
		{ChannelID: second, ModelName: "m", Outcome: OutcomeFatal},
	}, 0, false)
	require.Greater(t, mgr.EffectiveWeight(first, 10), 0.0,
		"one charged failure is below the cooldown threshold")

	// Second exhausted request reaches the threshold for both channels.
	mgr.RecordRequestAttempts([]ChannelAttempt{
		{ChannelID: first, ModelName: "m", Outcome: OutcomeFatal},
		{ChannelID: second, ModelName: "m", Outcome: OutcomeFatal},
	}, 0, false)

	assert.Zero(t, mgr.EffectiveWeight(first, 10), "the second charged failure ejects it")
	assert.Zero(t, mgr.EffectiveWeight(second, 10))
}

// TestRecordRequestAttemptsKeepsNeutralInert pins that batching did not weaken the
// classification contract: a caller-side 400 is still not evidence against the
// channel, even on a request that ultimately failed.
func TestRecordRequestAttemptsKeepsNeutralInert(t *testing.T) {
	mgr := resetHealthManager()
	now := time.Unix(1_700_000_000, 0)
	cfg := cooldownTestSetting()
	cfg.MinRequests = 0
	cfg.CooldownThreshold = 1
	configureCooldownTest(t, cfg, &now)

	const channelID = 9605
	for range 5 {
		mgr.RecordRequestAttempts([]ChannelAttempt{
			{ChannelID: channelID, ModelName: "m", Outcome: OutcomeNeutral},
		}, 0, false)
	}

	assert.Greater(t, mgr.EffectiveWeight(channelID, 10), 0.0,
		"neutral outcomes never eject a channel")
	assert.Equal(t, 1.0, mgr.GetScore(channelID))
}

// TestCooldownSaturationDisablesModelNotChannel is the escalation contract: a
// cooldown that keeps repeating never terminates on its own, so the pair is
// eventually disabled. It must be the model on that channel, not the channel, so
// the channel keeps serving whatever else it still can.
func TestCooldownSaturationDisablesModelNotChannel(t *testing.T) {
	mgr := resetHealthManager()
	now := time.Unix(1_700_000_000, 0)
	cfg := cooldownTestSetting()
	cfg.MinRequests = 0
	cfg.CooldownThreshold = 1
	cfg.CooldownDisableStreak = 3
	configureCooldownTest(t, cfg, &now)
	read := withCapturedModelDisabler(t)

	const channelID = 9606
	const dead = "dead-model"

	// Each exhausted request triggers one cooldown; expiry between them keeps the
	// channel selectable so the next request can charge it again.
	for i := range 3 {
		mgr.RecordRequestAttempts([]ChannelAttempt{
			{ChannelID: channelID, ModelName: dead, Outcome: OutcomeFatal},
		}, 0, false)
		if i < 2 {
			require.Empty(t, read(), "escalation must wait for the configured streak")
			now = now.Add(61 * time.Second)
		}
	}

	calls := awaitDisableCalls(t, read, 1)
	require.Len(t, calls, 1, "saturation disables the pair exactly once")
	assert.Equal(t, channelID, calls[0].channelID)
	assert.Equal(t, dead, calls[0].modelName,
		"the model is disabled, not the channel")
}

// TestCooldownSaturationAttributesPerModel proves the counter is per model: a
// channel whose one model is dead must not have a healthy sibling model disabled
// alongside it.
func TestCooldownSaturationAttributesPerModel(t *testing.T) {
	mgr := resetHealthManager()
	now := time.Unix(1_700_000_000, 0)
	cfg := cooldownTestSetting()
	cfg.MinRequests = 0
	cfg.CooldownThreshold = 1
	cfg.CooldownDisableStreak = 2
	configureCooldownTest(t, cfg, &now)
	read := withCapturedModelDisabler(t)

	const channelID = 9607

	// Alternating models: each reaches one cooldown, neither reaches two.
	for _, name := range []string{"model-a", "model-b"} {
		mgr.RecordRequestAttempts([]ChannelAttempt{
			{ChannelID: channelID, ModelName: name, Outcome: OutcomeFatal},
		}, 0, false)
		now = now.Add(61 * time.Second)
	}
	require.Empty(t, read(), "one cooldown each is below the streak for both models")

	// model-a fails again and crosses its own streak.
	mgr.RecordRequestAttempts([]ChannelAttempt{
		{ChannelID: channelID, ModelName: "model-a", Outcome: OutcomeFatal},
	}, 0, false)

	calls := awaitDisableCalls(t, read, 1)
	require.Len(t, calls, 1)
	assert.Equal(t, "model-a", calls[0].modelName,
		"only the model that actually saturated is disabled")
}

// TestCooldownSaturationDisabledByZeroStreak keeps the escalation opt-out honest:
// zero means the previous behaviour, cooldown forever and never disable.
func TestCooldownSaturationDisabledByZeroStreak(t *testing.T) {
	mgr := resetHealthManager()
	now := time.Unix(1_700_000_000, 0)
	cfg := cooldownTestSetting()
	cfg.MinRequests = 0
	cfg.CooldownThreshold = 1
	cfg.CooldownDisableStreak = 0
	configureCooldownTest(t, cfg, &now)
	read := withCapturedModelDisabler(t)

	const channelID = 9608
	for range 6 {
		mgr.RecordRequestAttempts([]ChannelAttempt{
			{ChannelID: channelID, ModelName: "m", Outcome: OutcomeFatal},
		}, 0, false)
		now = now.Add(61 * time.Second)
	}

	assert.Empty(t, awaitDisableCalls(t, read, 1),
		"a zero streak must never escalate to a disable")
}

// TestCooldownSaturationRetriesAfterFailedDisable covers the transient-DB-error
// path. The counter is cleared before the write so a slow write cannot escalate
// twice, which would silently drop the escalation if the write then failed; the
// count is restored instead, so the next cooldown tries again.
func TestCooldownSaturationRetriesAfterFailedDisable(t *testing.T) {
	mgr := resetHealthManager()
	now := time.Unix(1_700_000_000, 0)
	cfg := cooldownTestSetting()
	cfg.MinRequests = 0
	cfg.CooldownThreshold = 1
	cfg.CooldownDisableStreak = 1
	configureCooldownTest(t, cfg, &now)

	var mu sync.Mutex
	var calls int
	previous := channelModelDisabler
	channelModelDisabler = func(int, string) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return assert.AnError
		}
		return nil
	}
	t.Cleanup(func() { channelModelDisabler = previous })
	attempts := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}

	const channelID = 9610
	mgr.RecordRequestAttempts([]ChannelAttempt{
		{ChannelID: channelID, ModelName: "m", Outcome: OutcomeFatal},
	}, 0, false)

	deadline := time.Now().Add(2 * time.Second)
	for attempts() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.Equal(t, 1, attempts(), "the first disable was attempted and failed")

	// The restored count means the very next cooldown escalates again.
	now = now.Add(61 * time.Second)
	mgr.RecordRequestAttempts([]ChannelAttempt{
		{ChannelID: channelID, ModelName: "m", Outcome: OutcomeFatal},
	}, 0, false)

	deadline = time.Now().Add(2 * time.Second)
	for attempts() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	assert.Equal(t, 2, attempts(), "a failed disable is retried on the next cooldown")
}

// TestRecordChannelOutcomeNeverEscalates pins that the legacy single-outcome API
// cannot trigger a disable: it carries no model name, so there is nothing to
// attribute the failure to.
func TestRecordChannelOutcomeNeverEscalates(t *testing.T) {
	mgr := resetHealthManager()
	now := time.Unix(1_700_000_000, 0)
	cfg := cooldownTestSetting()
	cfg.MinRequests = 0
	cfg.CooldownThreshold = 1
	cfg.CooldownDisableStreak = 2
	configureCooldownTest(t, cfg, &now)
	read := withCapturedModelDisabler(t)

	const channelID = 9609
	for range 6 {
		mgr.RecordChannelOutcome(channelID, OutcomeFatal)
		now = now.Add(61 * time.Second)
	}

	assert.Empty(t, awaitDisableCalls(t, read, 1),
		"an outcome with no model attribution must not disable anything")
}
