package model

import (
	"sync"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// ChannelHealthManager tracks per-channel EWMA success-rate scores in memory.
//
// A missing entry means full health (score=1.0), so a channel that never fails
// costs nothing to track. The score is updated on every request outcome via
// RecordOutcome, and read during channel selection via EffectiveWeight to
// proportionally scale the configured base weight.
//
// When the kill switch (ChannelHealthSetting.Enabled) is false, EffectiveWeight
// returns the base weight unchanged, instantly restoring baseline behavior.
type ChannelHealthManager struct {
	mu     sync.Mutex
	states map[int]*channelHealthState
}

type channelHealthState struct {
	ewmaScore    float64 // range [MinScore, 1.0]
	requestCount int     // guard: don't trust EWMA until min_requests reached
}

var channelHealthOnce sync.Once
var channelHealth *ChannelHealthManager

// GetChannelHealthManager returns the singleton manager.
func GetChannelHealthManager() *ChannelHealthManager {
	channelHealthOnce.Do(func() {
		channelHealth = &ChannelHealthManager{
			states: make(map[int]*channelHealthState),
		}
	})
	return channelHealth
}

// defaultScore is the score a channel holds when it has no history or fewer
// than min_requests observations. It means "trust the channel until we have
// enough data to judge."
const defaultScore = 1.0

// RecordOutcome updates the EWMA score for a channel after a request.
// success=true means the request succeeded; false means it failed.
// This is safe to call concurrently; the mutex serializes updates per channel.
func (m *ChannelHealthManager) RecordOutcome(channelID int, success bool) {
	cfg := operation_setting.GetChannelHealthSetting()
	if cfg == nil || !cfg.Enabled {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.states[channelID]
	if !ok {
		state = &channelHealthState{
			ewmaScore:    defaultScore,
			requestCount: 0,
		}
		m.states[channelID] = state
	}

	state.requestCount++

	// Don't update EWMA until we have enough data; trust the channel.
	if state.requestCount <= cfg.MinRequests {
		return
	}

	outcome := 0.0
	if success {
		outcome = 1.0
	}

	state.ewmaScore = cfg.Alpha*outcome + (1-cfg.Alpha)*state.ewmaScore
	if state.ewmaScore < cfg.MinScore {
		state.ewmaScore = cfg.MinScore
	}
}

// EffectiveWeight returns the routing weight for a channel, scaled by its
// EWMA health score. When the kill switch is off, returns baseWeight unchanged.
func (m *ChannelHealthManager) EffectiveWeight(channelID int, baseWeight uint) float64 {
	cfg := operation_setting.GetChannelHealthSetting()
	if cfg == nil || !cfg.Enabled {
		return float64(baseWeight)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.states[channelID]
	if !ok {
		return float64(baseWeight) // no history = full health
	}

	return float64(baseWeight) * state.ewmaScore
}

// Reset clears all health state. Called when the kill switch is toggled off
// to ensure a clean slate when re-enabled.
func (m *ChannelHealthManager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states = make(map[int]*channelHealthState)
}

// GetScore returns the current EWMA score for diagnostics (e.g., admin API).
func (m *ChannelHealthManager) GetScore(channelID int) float64 {
	cfg := operation_setting.GetChannelHealthSetting()
	if cfg == nil || !cfg.Enabled {
		return defaultScore
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.states[channelID]
	if !ok {
		return defaultScore
	}
	return state.ewmaScore
}

