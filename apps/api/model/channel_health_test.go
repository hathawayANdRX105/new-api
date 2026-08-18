package model

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// resetHealthManager creates a fresh singleton for testing.
func resetHealthManager() *ChannelHealthManager {
	channelHealthOnce = sync.Once{}
	return GetChannelHealthManager()
}

func setTestConfig(enabled bool, alpha, minScore float64, minRequests int) {
	operation_setting.SetChannelHealthSetting(&operation_setting.ChannelHealthSetting{
		Enabled:     enabled,
		Alpha:       alpha,
		MinScore:    minScore,
		MinRequests: minRequests,
	})
}

// === EWMA Core Tests ===

func TestRecordOutcome_NewChannelFullHealth(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 5)
	w := mgr.EffectiveWeight(999, 100)
	assert.InDelta(t, 100.0, w, 0.001)
}

func TestRecordOutcome_MinRequestsGuard(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 5)
	for range 5 {
		mgr.RecordOutcome(1, false)
	}
	w := mgr.EffectiveWeight(1, 100)
	assert.InDelta(t, 100.0, w, 0.001, "score should not change within min_requests")
}

func TestRecordOutcome_FailureLowersScore(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)
	for range 10 {
		mgr.RecordOutcome(1, false)
	}
	score := mgr.GetScore(1)
	assert.LessOrEqual(t, score, 0.06)
	assert.GreaterOrEqual(t, score, 0.049)
}

func TestRecordOutcome_SuccessRaisesScore(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)
	for range 10 {
		mgr.RecordOutcome(1, false)
	}
	lowScore := mgr.GetScore(1)
	require.Less(t, lowScore, 0.1)
	for range 50 {
		mgr.RecordOutcome(1, true)
	}
	highScore := mgr.GetScore(1)
	assert.Greater(t, highScore, lowScore)
	assert.InDelta(t, 1.0, highScore, 0.01)
}

func TestEffectiveWeight_KillSwitch(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)
	for range 20 {
		mgr.RecordOutcome(1, false)
	}
	healthW := mgr.EffectiveWeight(1, 100)
	assert.Less(t, healthW, 10.0)
	setTestConfig(false, 0.3, 0.05, 0)
	baselineW := mgr.EffectiveWeight(1, 100)
	assert.InDelta(t, 100.0, baselineW, 0.001)
}

func TestEffectiveWeight_ZeroBaseWeight(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)
	w := mgr.EffectiveWeight(1, 0)
	assert.InDelta(t, 0.0, w, 0.001)
}

// === Simulation Scenarios (from sim_v5 + sim_v6) ===

// Scenario 1: Single channel outage
func TestScenario_SingleChannelOutage(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)
	for range 100 {
		mgr.RecordOutcome(1, true)
		mgr.RecordOutcome(2, true)
		mgr.RecordOutcome(3, false)
	}
	w1 := mgr.EffectiveWeight(1, 10)
	w2 := mgr.EffectiveWeight(2, 10)
	w3 := mgr.EffectiveWeight(3, 10)
	assert.Greater(t, w1, w3)
	assert.Greater(t, w2, w3)
	assert.Less(t, w3, 1.0)
}

// Scenario 2: Intermittent 15% failure — score stays high
func TestScenario_Intermittent15Percent(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)
	for i := range 200 {
		if i%7 == 0 {
			mgr.RecordOutcome(1, false)
		} else {
			mgr.RecordOutcome(1, true)
		}
	}
	score := mgr.GetScore(1)
	assert.Greater(t, score, 0.7)
	assert.Less(t, score, 1.0)
}

// Scenario 3: All channels degraded 15% — no death spiral
func TestScenario_AllDegraded15Percent_NoDeathSpiral(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)
	for i := range 300 {
		for chID := 1; chID <= 3; chID++ {
			if i%7 == 0 {
				mgr.RecordOutcome(chID, false)
			} else {
				mgr.RecordOutcome(chID, true)
			}
		}
	}
	for chID := 1; chID <= 3; chID++ {
		w := mgr.EffectiveWeight(chID, 10)
		assert.Greater(t, w, 0.3, "channel %d should not spiral to zero", chID)
	}
}

// Scenario 4: High failure 40%
func TestScenario_HighFail40Percent(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)
	for i := range 200 {
		if i%5 < 2 {
			mgr.RecordOutcome(1, false)
		} else {
			mgr.RecordOutcome(1, true)
		}
	}
	score := mgr.GetScore(1)
	assert.Greater(t, score, 0.4)
	assert.Less(t, score, 0.8)
}

// Scenario 5: All channels down — converge to min_score, not panic
func TestScenario_AllChannelsDown(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)
	for range 100 {
		for chID := 1; chID <= 3; chID++ {
			mgr.RecordOutcome(chID, false)
		}
	}
	for chID := 1; chID <= 3; chID++ {
		score := mgr.GetScore(chID)
		assert.InDelta(t, 0.05, score, 0.01)
		w := mgr.EffectiveWeight(chID, 10)
		assert.Greater(t, w, 0.0, "channel %d should have non-zero weight (floor)", chID)
	}
}

// Scenario 6: Kill switch instant restore
func TestScenario_KillSwitchInstantRestore(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)
	for range 50 {
		mgr.RecordOutcome(1, false)
	}
	lowW := mgr.EffectiveWeight(1, 100)
	require.Less(t, lowW, 20.0)
	setTestConfig(false, 0.3, 0.05, 0)
	baselineW := mgr.EffectiveWeight(1, 100)
	assert.InDelta(t, 100.0, baselineW, 0.001)
}

// Scenario 7: Recovery speed — ~100 requests to fully recover
func TestScenario_RecoverySpeed(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)
	for range 100 {
		mgr.RecordOutcome(1, false)
	}
	lowScore := mgr.GetScore(1)
	require.Less(t, lowScore, 0.1)
	// Recover
	for range 100 {
		mgr.RecordOutcome(1, true)
	}
	recoveredScore := mgr.GetScore(1)
	assert.Greater(t, recoveredScore, 0.9, "should recover to near-full after 100 successes")
}

// Scenario 8: Partial recovery — all down then 1 recovers
func TestScenario_PartialRecovery(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)
	// All fail
	for range 50 {
		for chID := 1; chID <= 3; chID++ {
			mgr.RecordOutcome(chID, false)
		}
	}
	// Ch1 recovers, ch2+3 still fail
	for range 100 {
		mgr.RecordOutcome(1, true)
		mgr.RecordOutcome(2, false)
		mgr.RecordOutcome(3, false)
	}
	w1 := mgr.EffectiveWeight(1, 10)
	w2 := mgr.EffectiveWeight(2, 10)
	w3 := mgr.EffectiveWeight(3, 10)
	assert.Greater(t, w1, w2, "recovered ch1 should have higher weight")
	assert.Greater(t, w1, w3)
}

// Scenario 9: Long run numerical stability — no drift after 10000 requests
func TestScenario_LongRunStability(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)
	for i := range 10000 {
		if i%10 == 0 { // 10% fail
			mgr.RecordOutcome(1, false)
		} else {
			mgr.RecordOutcome(1, true)
		}
	}
	score := mgr.GetScore(1)
	// EWMA should converge to ~0.9 for 10% fail rate
	assert.Greater(t, score, 0.85)
	assert.Less(t, score, 1.0)
}

// Scenario 10: Alpha=1.0 degrades to last-outcome
func TestScenario_AlphaOneLastOutcome(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 1.0, 0.05, 0)
	// 10 successes → score=1.0
	for range 10 {
		mgr.RecordOutcome(1, true)
	}
	assert.InDelta(t, 1.0, mgr.GetScore(1), 0.001)
	// 1 failure → score=0.05 (min)
	mgr.RecordOutcome(1, false)
	assert.InDelta(t, 0.05, mgr.GetScore(1), 0.001)
	// 1 success → score=1.0
	mgr.RecordOutcome(1, true)
	assert.InDelta(t, 1.0, mgr.GetScore(1), 0.001)
}

// Scenario 11: Alpha=0.01 reacts very slowly
func TestScenario_AlphaLowReactSlow(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.01, 0.05, 0)
	// 50 failures with alpha=0.01 — score drops very slowly
	for range 50 {
		mgr.RecordOutcome(1, false)
	}
	score := mgr.GetScore(1)
	// With alpha=0.01, after 50 failures: 0.99^50 ≈ 0.605
	assert.Greater(t, score, 0.5, "slow alpha should not drop fast")
	assert.Less(t, score, 0.7)
}

// Scenario 12: Weight=0 channel — effective weight is 0
func TestScenario_WeightZeroChannel(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)
	w := mgr.EffectiveWeight(1, 0)
	assert.InDelta(t, 0.0, w, 0.001)
}

// Scenario 13: New channel gets full weight immediately
func TestScenario_NewChannelFullWeight(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 5)
	// Channel 999 has no history
	w := mgr.EffectiveWeight(999, 50)
	assert.InDelta(t, 50.0, w, 0.001, "new channel should get full base weight")
}

// === Config Validation ===

func TestSetChannelHealthSetting_Clamps(t *testing.T) {
	operation_setting.SetChannelHealthSetting(&operation_setting.ChannelHealthSetting{
		Enabled:     true,
		Alpha:       5.0,  // should clamp to 1.0
		MinScore:    -1.0, // should clamp to 0
		MinRequests: -5,   // should clamp to 0
	})
	cfg := operation_setting.GetChannelHealthSetting()
	assert.InDelta(t, 1.0, cfg.Alpha, 0.001)
	assert.InDelta(t, 0.0, cfg.MinScore, 0.001)
	assert.Equal(t, 0, cfg.MinRequests)
}

// === Concurrency Test ===

func TestRecordOutcome_ConcurrentSafety(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)
	var wg sync.WaitGroup
	for g := 0; g < 100; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range 100 {
				mgr.RecordOutcome(id%3+1, i%2 == 0)
			}
		}(g)
	}
	wg.Wait()
	// Should not panic, scores should be valid
	for chID := 1; chID <= 3; chID++ {
		score := mgr.GetScore(chID)
		assert.GreaterOrEqual(t, score, 0.04)
		assert.LessOrEqual(t, score, 1.01)
	}
}
