package model

import (
	"sync"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// ChannelHealthManager tracks per-channel EWMA success-rate scores in memory.

var channelHealthOnce sync.Once
var channelHealth *ChannelHealthManager

// A missing entry means full health (score=1.0), so a channel that never fails
// costs nothing to track. The score is updated on every request outcome via
// RecordOutcome, and read during channel selection via EffectiveWeight to
// proportionally scale the configured base weight.

// When the kill switch (ChannelHealthSetting.Enabled) is false, EffectiveWeight
// returns the base weight unchanged, instantly restoring baseline behavior.
type ChannelHealthManager struct {
	mu     sync.Mutex
	states map[int]*channelHealthState
}

type channelHealthState struct {
	ewmaScore       float64 // range [MinScore, 1.0]
	requestCount    int     // guard: don't trust EWMA until min_requests reached
	unauthorizedRun int     // consecutive 401s for escalation classification
	rampExited      bool    // slow-start warm-up abandoned after a real failure
}

// Wire the kill-switch cleanup here rather than in operation_setting, which must
// not import this package. Toggling the switch off now discards accumulated
// scores so re-enabling starts from a clean slate instead of resurrecting them.
func init() {
	operation_setting.RegisterHealthStateResetHook(func() {
		GetChannelHealthManager().Reset()
	})
}

// ChannelOutcome categorizes the result of a channel request for routing and
// health-score purposes. Outcomes are used by ClassifyChannelOutcome and
// RecordChannelOutcome; they do not affect the existing RecordOutcome API.
type ChannelOutcome int

const (
	// OutcomeSuccess means the request completed successfully (2xx response).
	// This is the healthiest outcome; the channel is fully trusted.
	OutcomeSuccess ChannelOutcome = iota

	// OutcomeFatal means the channel has a serious error (5xx, bad response body,
	// or local channel:* errors). The channel should be excluded from routing.
	OutcomeFatal

	// OutcomeThrottled means the channel received a 429 response. The channel is
	// healthy but currently rate-limited. A mild derate is applied; the channel
	// can recover with successful requests.
	OutcomeThrottled

	// OutcomeNeutral means the request resulted in a non-fatal, non-throttling
	// error (400, 403, 404, 422, or isolated 401). The channel is neither
	// rewarded nor penalized; its score is unchanged.
	OutcomeNeutral
)

// AffectsHealth returns true if this outcome affects the channel's health score.
// Success, Fatal, and Throttled all update the EWMA; Neutral does not.
func (o ChannelOutcome) AffectsHealth() bool {
	return o == OutcomeSuccess || o == OutcomeFatal || o == OutcomeThrottled
}

// ExcludesChannel returns true if this outcome excludes the channel from
// routing consideration. Fatal and Throttled exclude the channel; Success and
// Neutral do not.
func (o ChannelOutcome) ExcludesChannel() bool {
	return o == OutcomeFatal || o == OutcomeThrottled
}

// unauthorizedEscalationThreshold is how many consecutive upstream 401s on one
// channel escalate from OutcomeNeutral to OutcomeFatal. A single 401 is usually
// a caller-side problem, but a sustained run means the channel credential is
// dead and will not self-heal. Three follows Envoy, whose
// consecutive_gateway_failure defaults to 3 while consecutive_5xx defaults to 5.
const unauthorizedEscalationThreshold = 3

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

// ClassifyChannelOutcome categorizes an API error into a ChannelOutcome. The
// function is concurrency-safe: it acquires the manager mutex and then calls
// the unlocked classifier. It does NOT respect the kill switch (ChannelHealthSetting.Enabled);
// classification drives request-level exclusion, which should always operate.
// Callers must not hold m.mu when calling this function.
func ClassifyChannelOutcome(err *types.NewAPIError, channelID int) ChannelOutcome {
	m := GetChannelHealthManager()
	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.states[channelID]
	if !ok {
		state = &channelHealthState{
			ewmaScore:       defaultScore,
			requestCount:    0,
			unauthorizedRun: 0,
		}
		m.states[channelID] = state
	}

	return classifyChannelOutcomeUnlocked(state, err)
}

// classifyChannelOutcomeUnlocked classifies the error assuming the manager mutex
// is already held by the caller. It does NOT acquire the lock itself.
func classifyChannelOutcomeUnlocked(state *channelHealthState, err *types.NewAPIError) ChannelOutcome {
	if err == nil {
		state.unauthorizedRun = 0
		return OutcomeSuccess
	}

	// Channel errors or bad response body are always fatal.
	if types.IsChannelError(err) || err.GetErrorCode() == types.ErrorCodeBadResponseBody {
		state.unauthorizedRun = 0
		return OutcomeFatal
	}

	// 429 => throttled, reset unauthorized run.
	if err.StatusCode == 429 {
		state.unauthorizedRun = 0
		return OutcomeThrottled
	}

	// 5xx => fatal.
	if err.StatusCode >= 500 {
		state.unauthorizedRun = 0
		return OutcomeFatal
	}

	// 401 => count the run and escalate once it is sustained. The counter is NOT
	// reset on escalation: a dead credential keeps returning 401, and every one of
	// those is fatal. Resetting here would make the run oscillate
	// Neutral, Neutral, Fatal, Neutral, Neutral, Fatal and penalise a dead channel
	// on only one request in three. The counter is cleared by any non-401 outcome,
	// which is what makes an isolated or flapping 401 harmless.
	if err.StatusCode == 401 {
		if state.unauthorizedRun < unauthorizedEscalationThreshold {
			state.unauthorizedRun++
		}
		if state.unauthorizedRun >= unauthorizedEscalationThreshold {
			return OutcomeFatal
		}
		return OutcomeNeutral
	}

	// All other status codes => neutral, reset unauthorized run.
	state.unauthorizedRun = 0
	return OutcomeNeutral
}

// throttledObservation is the EWMA observation fed for OutcomeThrottled. With
// the default Alpha of 0.3 a permanently throttled channel converges to this
// value, i.e. roughly a 30% derate, and climbs back to full health within about
// ten successful requests. A full 0.0 penalty would instead collapse it to
// MinScore and starve a channel that is merely busy.
const throttledObservation = 0.7

// RecordChannelOutcome updates the EWMA score for a channel based on a
// ChannelOutcome. Unlike RecordOutcome (which uses a success bool), this method
// accepts a ChannelOutcome and applies the appropriate observation value:
//
//	OutcomeSuccess  -> 1.0
//	OutcomeFatal    -> 0.0
//	OutcomeThrottled-> throttledObservation (0.7)
//	OutcomeNeutral  -> returns immediately, without incrementing requestCount
//	                  or modifying the score.
//
// The kill switch (ChannelHealthSetting.Enabled) gates health scoring only: when
// it is off this method returns without touching the score. Request-level
// exclusion is unaffected because it is driven by ClassifyChannelOutcome and the
// caller's ExcludeSet, neither of which consults the kill switch.
func (m *ChannelHealthManager) RecordChannelOutcome(channelID int, outcome ChannelOutcome) {
	cfg := operation_setting.GetChannelHealthSetting()
	if cfg == nil || !cfg.Enabled {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.states[channelID]
	if !ok {
		state = &channelHealthState{
			ewmaScore:       defaultScore,
			requestCount:    0,
			unauthorizedRun: 0,
		}
		m.states[channelID] = state
	}

	// Neutral outcomes do not increment requestCount or update the score.
	if outcome == OutcomeNeutral {
		return
	}

	// Apply the appropriate observation via the shared EWMA update.
	var observation float64
	switch outcome {
	case OutcomeSuccess:
		observation = 1.0
	case OutcomeFatal:
		observation = 0.0
	case OutcomeThrottled:
		observation = throttledObservation
	default:
		observation = 0.0
	}

	// Increment request count and update EWMA.
	state.requestCount++

	// A fatal outcome ends the warm-up ramp immediately: the channel has proven it
	// is broken, so it should not keep climbing toward full weight.
	if outcome == OutcomeFatal {
		state.rampExited = true
	}

	if state.requestCount <= cfg.MinRequests {
		return
	}

	state.ewmaScore = cfg.Alpha*observation + (1-cfg.Alpha)*state.ewmaScore
	if state.ewmaScore < cfg.MinScore {
		state.ewmaScore = cfg.MinScore
	}
}

// RecordOutcome updates the EWMA score for a channel after a request.
// success=true means the request succeeded; false means it failed.
// This is safe to call concurrently; the mutex serializes updates per channel.
// The kill switch (ChannelHealthSetting.Enabled) is checked: if disabled, the
// method returns immediately without modifying state. This preserves existing
// behavior where health scoring is gated by the kill switch.
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
			ewmaScore:       defaultScore,
			requestCount:    0,
			unauthorizedRun: 0,
		}
		m.states[channelID] = state
	}

	state.requestCount++

	// A real failure ends the warm-up ramp immediately, mirroring AWS ALB slow
	// start where a target leaves the ramp as soon as it looks unhealthy.
	if !success {
		state.rampExited = true
	}

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

// slowStartFactor scales a channel's routing weight during its warm-up window.
//
// The MinRequests guard keeps a fresh channel's score pinned at full health so a
// single early failure cannot condemn it. On its own that also means a channel
// failing every request still competes at full weight for its first MinRequests
// picks. Ramping the weight linearly over the window keeps the guard's caution
// while denying a broken channel that free ride, which is how AWS ALB, Envoy and
// Alibaba ASM implement slow start.
//
// Callers must hold m.mu.
func slowStartFactor(state *channelHealthState, minRequests int) float64 {
	if minRequests <= 0 || state.rampExited || state.requestCount >= minRequests {
		return 1.0
	}
	return float64(state.requestCount+1) / float64(minRequests)
}

// EffectiveWeight returns the routing weight for a channel, scaled by its EWMA
// health score and, while it is still warming up, by its slow-start factor.
// When the kill switch is off, returns baseWeight unchanged.
func (m *ChannelHealthManager) EffectiveWeight(channelID int, baseWeight uint) float64 {
	cfg := operation_setting.GetChannelHealthSetting()
	if cfg == nil || !cfg.Enabled {
		return float64(baseWeight)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.states[channelID]
	if !ok {
		return float64(baseWeight) // no history = full health, no ramp to apply
	}

	return float64(baseWeight) * state.ewmaScore * slowStartFactor(state, cfg.MinRequests)
}

// routingBaseWeight converts a configured channel weight into the base weight
// used for weighted-random routing. Both selection paths (the memory-cache path
// in GetRandomSatisfiedChannel and the DB path in GetChannel) MUST call this so
// MEMORY_CACHE_ENABLED cannot change traffic distribution.
//
// The +1 offset keeps weight=0 channels selectable at the lowest possible share
// instead of dropping them, while staying strictly monotone: a larger configured
// weight always yields a larger routing weight.
func routingBaseWeight(weight int) uint {
	if weight < 0 {
		return 1
	}
	return uint(weight) + 1
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
