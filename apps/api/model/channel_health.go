package model

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
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
	rampPending     bool    // post-cooldown first selection starts at the ramp floor

	// Cooldown ejection state. failureStreak counts consecutive
	// fatal/throttled outcomes and triggers a cooldown at the configured
	// threshold. cooldownStreak is the number of successive activations,
	// retained for the sliding-duration formula; it decays by one after a
	// clean post-expiry success. cooldownUntil is the lazy expiry deadline;
	// zero means "not cooling".
	failureStreak  int
	cooldownStreak int
	cooldownUntil  time.Time

	// modelCooldowns counts cooldown activations attributed to each model on
	// this channel. The sliding duration stays channel-level, but the disable
	// decision must not be: a channel whose gpt-4 is dead may still serve its
	// other models, so escalation needs to know which model kept failing.
	// Entries appear only for models that have actually caused a cooldown.
	modelCooldowns map[string]int
}

// channelHealthNow is the package clock, injected for deterministic tests.
// Production code reads it unchanged; tests replace it and restore it via
// t.Cleanup so no test leaks a frozen clock into another.
var channelHealthNow = time.Now

// channelModelDisabler disables one model on one channel once its cooldowns
// saturate. It is a package var so tests can capture the call instead of
// touching the database, and so the model layer keeps owning the DB write.
var channelModelDisabler = DisableChannelModel

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
//
// State is persisted only when the classifier actually needs to remember
// something, i.e. when a 401 run is in progress. Persisting unconditionally would
// leave a channel whose traffic is entirely OutcomeNeutral sitting at
// requestCount 0 forever, and slowStartFactor would then read that as a channel
// permanently stuck at the start of its warm-up ramp and derate it to
// 1/MinRequests of its configured weight indefinitely.
func ClassifyChannelOutcome(err *types.NewAPIError, channelID int) ChannelOutcome {
	m := GetChannelHealthManager()
	m.mu.Lock()
	defer m.mu.Unlock()

	state, tracked := m.states[channelID]
	if !tracked {
		state = &channelHealthState{ewmaScore: defaultScore}
	}

	outcome := classifyChannelOutcomeUnlocked(state, err)

	// A live 401 run is the only classification state worth carrying between
	// requests; everything else is derived fresh from the error each time.
	if !tracked && state.unauthorizedRun > 0 {
		m.states[channelID] = state
	}

	return outcome
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
	m.recordChannelOutcome(channelID, "", outcome)
}

// recordChannelOutcome is the model-aware form. An empty modelName records
// health exactly as before but cannot escalate to a per-model disable, since
// there is nothing to attribute the failure to.
func (m *ChannelHealthManager) recordChannelOutcome(channelID int, modelName string, outcome ChannelOutcome) {
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

	now := channelHealthNow()

	// Finish any expired cooldown before applying the new outcome so a
	// post-expiry result re-enters the slow-start curve cleanly.
	if !state.cooldownUntil.IsZero() && !state.cooldownUntil.After(now) {
		finishCooldownLocked(state)
	}

	switch outcome {
	case OutcomeSuccess:
		state.failureStreak = 0
		// A clean success once a cooldown has expired decays the streak so the
		// next failure run starts from a shorter duration.
		if state.cooldownStreak > 0 && state.cooldownUntil.IsZero() {
			state.cooldownStreak--
		}
	case OutcomeNeutral:
		// Neutral stays score/request-count inert but clears an accumulated
		// failure streak: it is not evidence against the channel.
		state.failureStreak = 0
		return
	case OutcomeFatal, OutcomeThrottled:
		state.failureStreak++
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
	state.rampPending = false

	// A fatal outcome ends the warm-up ramp immediately: the channel has proven it
	// is broken, so it should not keep climbing toward full weight.
	if outcome == OutcomeFatal {
		state.rampExited = true
	}

	if state.requestCount > cfg.MinRequests {
		state.ewmaScore = cfg.Alpha*observation + (1-cfg.Alpha)*state.ewmaScore
		if state.ewmaScore < cfg.MinScore {
			state.ewmaScore = cfg.MinScore
		}
	}

	// The cooldown trigger is deliberately outside the MinRequests guard: a
	// channel failing every request from cold must still be ejected.
	if (outcome == OutcomeFatal || outcome == OutcomeThrottled) && state.failureStreak >= cfg.CooldownThreshold {
		startCooldownLocked(state, cfg, now)
		if modelName != "" {
			m.escalateModelLocked(state, cfg, channelID, modelName)
		}
	}
}

// escalateModelLocked counts this cooldown against modelName and disables that
// model on the channel once the count reaches CooldownDisableStreak. Callers
// must hold m.mu.
//
// The DB write runs in a goroutine because m.mu is held and the write path takes
// its own transaction plus a routing-revision bump; blocking every caller of the
// health manager on that would serialize request handling behind a disk write.
//
// The counter is cleared before the write so a slow write cannot escalate twice
// while it is still in flight, and restored if the write fails, so a transient DB
// error only delays the disable to the next cooldown instead of losing it.
func (m *ChannelHealthManager) escalateModelLocked(state *channelHealthState, cfg *operation_setting.ChannelHealthSetting, channelID int, modelName string) {
	if cfg.CooldownDisableStreak <= 0 {
		return
	}
	if state.modelCooldowns == nil {
		state.modelCooldowns = make(map[string]int, 1)
	}
	state.modelCooldowns[modelName]++
	if state.modelCooldowns[modelName] < cfg.CooldownDisableStreak {
		return
	}
	reached := state.modelCooldowns[modelName]
	delete(state.modelCooldowns, modelName)
	disable := channelModelDisabler
	go func() {
		if err := disable(channelID, modelName); err != nil {
			common.SysError(fmt.Sprintf("failed to disable model %q on channel %d after repeated cooldowns: %s",
				modelName, channelID, err.Error()))
			// Put the count back so the next cooldown retries the disable rather
			// than starting the streak over.
			m.mu.Lock()
			defer m.mu.Unlock()
			if current, ok := m.states[channelID]; ok {
				if current.modelCooldowns == nil {
					current.modelCooldowns = make(map[string]int, 1)
				}
				if current.modelCooldowns[modelName] < reached {
					current.modelCooldowns[modelName] = reached
				}
			}
			return
		}
		common.SysLog(fmt.Sprintf("model %q disabled on channel %d: cooldown repeated %d times without recovery",
			modelName, channelID, reached))
	}()
}

// ChannelAttempt is one channel try inside a single client request. The relay
// loop collects these so health accounting can wait until the request's final
// outcome is known. ModelName is what the attempt asked the channel for, which
// is what a repeated-cooldown disable is attributed to.
type ChannelAttempt struct {
	ChannelID int
	ModelName string
	Outcome   ChannelOutcome
}

// RecordRequestAttempts applies health accounting once for a whole client
// request, rather than once per failed try.
//
// A 429 or 5xx that a retry recovered from cost the caller nothing: another
// channel served the request. Charging that against the first channel would
// drive cooldown on channels that are merely busy, which is the opposite of what
// the caller experienced. A request is only a real failure once every retry is
// exhausted, and only then do its attempts count against the channels involved.
//
// succeeded reports whether the request ultimately returned a result. When it
// did, winnerID takes the success observation and the earlier failed attempts
// are discarded. When it did not, every attempt is applied in the order it was
// made, so the failure streaks and cooldown timers advance exactly as they would
// have without the deferral.
//
// Request-level exclusion is unaffected: the relay loop still classifies each
// failure immediately and populates its ExcludeSet, so a retry never reselects
// the channel that just failed.
func (m *ChannelHealthManager) RecordRequestAttempts(attempts []ChannelAttempt, winnerID int, succeeded bool) {
	if succeeded {
		// ClassifyChannelOutcome(nil, ...) also clears any in-flight 401 run,
		// which a bare OutcomeSuccess would leave standing.
		m.RecordChannelOutcome(winnerID, ClassifyChannelOutcome(nil, winnerID))
		return
	}
	for _, attempt := range attempts {
		if attempt.Outcome.AffectsHealth() {
			m.recordChannelOutcome(attempt.ChannelID, attempt.ModelName, attempt.Outcome)
		}
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
	// requestCount == 0 means no scored outcome has been observed yet. Such an
	// entry is indistinguishable from a channel with no entry at all (which
	// EffectiveWeight short-circuits to full weight), so it must not be derated.
	// State can exist at zero count because ClassifyChannelOutcome tracks 401
	// runs, and a run that later clears leaves the entry behind. A cooldown
	// expiry is the deliberate exception: rampPending starts the recovered
	// channel at the first ramp step so it is probed rather than flooded.
	//
	// From the first observed outcome onward the factor is requestCount/minRequests:
	// after one outcome a five-request window yields 1/5, and the window completes
	// at requestCount == minRequests. Using the count already observed (rather than
	// count+1) keeps the curve monotone across the zero-count boundary.
	if minRequests <= 0 || state.rampExited {
		return 1.0
	}
	if state.rampPending {
		return 1.0 / float64(minRequests)
	}
	if state.requestCount == 0 || state.requestCount >= minRequests {
		return 1.0
	}
	return float64(state.requestCount) / float64(minRequests)
}

// EffectiveWeight returns the routing weight for a channel, scaled by its EWMA
// health score and, while it is still warming up, by its slow-start factor. A
// channel in a live cooldown weighs zero, so weighted-random selection ejects it
// entirely. When the kill switch is off, returns baseWeight unchanged.
func (m *ChannelHealthManager) EffectiveWeight(channelID int, baseWeight uint) float64 {
	return m.routingWeight(channelID, baseWeight, false)
}

// routingWeight is the shared health/cooldown weight path. EffectiveWeight calls
// it with bypassCooldown=false. Selectors call it with true only for cooling
// candidates deliberately retained by the max-ejection cap; ejected candidates
// are dropped from the candidate set instead.
func (m *ChannelHealthManager) routingWeight(channelID int, baseWeight uint, bypassCooldown bool) float64 {
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

	now := channelHealthNow()
	if !state.cooldownUntil.IsZero() {
		if state.cooldownUntil.After(now) {
			if !bypassCooldown {
				return 0
			}
		} else {
			// Expired: finish it lazily so the recovered channel re-enters the
			// slow-start curve on this very selection.
			finishCooldownLocked(state)
		}
	}

	return float64(baseWeight) * state.ewmaScore * slowStartFactor(state, cfg.MinRequests)
}

// cooldownDuration computes the sliding cooldown duration. The formula
// base + (max-base)*(1-alpha^priorActivations) yields exactly base for the first
// activation and approaches max as activations accumulate. A non-positive base or
// max yields zero, which disables cooldown.
func cooldownDuration(cfg *operation_setting.ChannelHealthSetting, priorActivations int) time.Duration {
	base, max := cfg.CooldownBaseSeconds, cfg.CooldownMaxSeconds
	if base <= 0 || max <= 0 {
		return 0
	}
	if max < base {
		max = base
	}
	// alpha^0 == 1, so the first activation is exactly base.
	factor := 1.0 - math.Pow(cfg.CooldownAlpha, float64(priorActivations))
	d := float64(base) + float64(max-base)*factor
	if d < float64(base) {
		d = float64(base)
	}
	if d > float64(max) {
		d = float64(max)
	}
	return time.Duration(d * float64(time.Second))
}

// startCooldownLocked activates a cooldown sized from the current cooldownStreak,
// then increments the streak so a repeat offender cools longer. Callers must hold
// m.mu. A zero-duration configuration disables cooldown without disturbing EWMA.
func startCooldownLocked(state *channelHealthState, cfg *operation_setting.ChannelHealthSetting, now time.Time) {
	d := cooldownDuration(cfg, state.cooldownStreak)
	if d <= 0 {
		return
	}
	state.cooldownStreak++
	state.failureStreak = 0
	state.requestCount = 0
	state.rampExited = false
	state.cooldownUntil = now.Add(d)
}

// finishCooldownLocked clears an expired cooldown and arms the slow-start ramp so
// the recovered channel is probed at 1/MinRequests of its weight before climbing
// back. Callers must hold m.mu.
func finishCooldownLocked(state *channelHealthState) {
	state.cooldownUntil = time.Time{}
	state.requestCount = 0
	state.rampExited = false
	state.rampPending = true
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

// FilterCoolingChannels reports which of channelIDs must be removed from a
// priority tier because they are in a live cooldown.
//
// A partially cooling tier is capped: at most maxEjectionPercent of the tier is
// removed (floored, but always at least one), and the rest stay selectable as a
// degraded availability fallback whose weight the selector computes with
// bypassCooldown=true. Cooling IDs are sorted ascending so the choice is
// deterministic. A fully cooling tier is the safety exception and is ejected
// wholesale, so selection descends to the next priority tier or fails fast
// instead of retaining a channel that is certain to fail — that unbounded retry
// against a dead tier is the hot loop this cooldown exists to stop.
//
// Ejection is disabled entirely when the kill switch is off or
// maxEjectionPercent <= 0. Expired timers encountered here are finished lazily.
func (m *ChannelHealthManager) FilterCoolingChannels(channelIDs []int, maxEjectionPercent int) map[int]bool {
	ejected := make(map[int]bool)
	cfg := operation_setting.GetChannelHealthSetting()
	if cfg == nil || !cfg.Enabled || maxEjectionPercent <= 0 {
		return ejected
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := channelHealthNow()

	var cooling []int
	for _, id := range channelIDs {
		state, ok := m.states[id]
		if !ok || state.cooldownUntil.IsZero() {
			continue
		}
		if !state.cooldownUntil.After(now) {
			finishCooldownLocked(state)
			continue
		}
		cooling = append(cooling, id)
	}
	if len(cooling) == 0 {
		return ejected
	}

	// Fully cooling tier, or an operator asking for total ejection: drop all.
	if len(cooling) == len(channelIDs) || maxEjectionPercent >= 100 {
		for _, id := range cooling {
			ejected[id] = true
		}
		return ejected
	}

	sort.Ints(cooling)

	ejectLimit := len(channelIDs) * maxEjectionPercent / 100 // floor
	if ejectLimit < 1 {
		ejectLimit = 1
	}
	if ejectLimit > len(cooling) {
		ejectLimit = len(cooling)
	}
	for _, id := range cooling[:ejectLimit] {
		ejected[id] = true
	}
	return ejected
}
