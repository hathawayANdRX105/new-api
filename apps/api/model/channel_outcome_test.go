package model

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuantumNous/new-api/relaykit/types"
)

// upstreamError builds the kind of error the relay layer produces for an upstream
// HTTP failure, i.e. one whose error code carries no "channel:" prefix.
func upstreamError(code types.ErrorCode, status int) *types.NewAPIError {
	return types.NewErrorWithStatusCode(errors.New("simulated upstream failure"), code, status)
}

// TestClassifyChannelOutcome_ByErrorClass pins the classification contract.
// Before this change relay.go penalised a failure only when types.IsChannelError
// was true, so every upstream 5xx, 429 and empty body was invisible to the health
// score. Each case uses a distinct channel id so the 401 run counter of one case
// cannot leak into another.
func TestClassifyChannelOutcome_ByErrorClass(t *testing.T) {
	resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 5)

	cases := []struct {
		name   string
		err    *types.NewAPIError
		expect ChannelOutcome
	}{
		{"nil error is success", nil, OutcomeSuccess},
		{"upstream 500", upstreamError("upstream_error", 500), OutcomeFatal},
		{"upstream 502", upstreamError("upstream_error", 502), OutcomeFatal},
		{"upstream 504", upstreamError("upstream_error", 504), OutcomeFatal},
		{"empty 200 body", upstreamError(types.ErrorCodeBadResponseBody, 200), OutcomeFatal},
		{"429 throttled", upstreamError("rate_limit_exceeded", 429), OutcomeThrottled},
		{"400 caller bug", upstreamError("invalid_request_error", 400), OutcomeNeutral},
		{"403 forbidden", upstreamError("permission_error", 403), OutcomeNeutral},
		{"404 model missing", upstreamError("not_found", 404), OutcomeNeutral},
		{"422 unprocessable", upstreamError("invalid_request_error", 422), OutcomeNeutral},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Distinct channel id per case: the 401 counter is per channel.
			assert.Equal(t, tc.expect, ClassifyChannelOutcome(tc.err, 7000+i))
		})
	}
}

// TestClassifyChannelOutcome_ChannelErrorsStayFatal covers all seven channel:*
// codes, the only failures the merged code ever penalised. They must remain fatal.
func TestClassifyChannelOutcome_ChannelErrorsStayFatal(t *testing.T) {
	resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 5)

	codes := []types.ErrorCode{
		types.ErrorCodeChannelNoAvailableKey,
		types.ErrorCodeChannelParamOverrideInvalid,
		types.ErrorCodeChannelHeaderOverrideInvalid,
		types.ErrorCodeChannelModelMappedError,
		types.ErrorCodeChannelAwsClientError,
		types.ErrorCodeChannelInvalidKey,
		types.ErrorCodeChannelResponseTimeExceeded,
	}

	for i, code := range codes {
		err := upstreamError(code, 500)
		require.True(t, types.IsChannelError(err), "code %q must be a channel error", code)
		assert.Equal(t, OutcomeFatal, ClassifyChannelOutcome(err, 7100+i), "code %q", code)
	}
}

// TestClassifyChannelOutcome_UnauthorizedEscalatesOnRun asserts the 401 policy:
// an isolated 401 says nothing about the channel, but a sustained run means the
// credential is dead. Threshold 3 follows Envoy's consecutive_gateway_failure.
func TestClassifyChannelOutcome_UnauthorizedEscalatesOnRun(t *testing.T) {
	resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 5)

	const channelID = 7200
	unauthorized := upstreamError("authentication_error", 401)

	assert.Equal(t, OutcomeNeutral, ClassifyChannelOutcome(unauthorized, channelID), "401 #1")
	assert.Equal(t, OutcomeNeutral, ClassifyChannelOutcome(unauthorized, channelID), "401 #2")
	assert.Equal(t, OutcomeFatal, ClassifyChannelOutcome(unauthorized, channelID), "401 #3 escalates")

	// The run must latch: a dead credential keeps returning 401 and every one of
	// those is fatal. If the counter reset on escalation the channel would only be
	// penalised on one request in three.
	assert.Equal(t, OutcomeFatal, ClassifyChannelOutcome(unauthorized, channelID), "401 #4 stays fatal")
	assert.Equal(t, OutcomeFatal, ClassifyChannelOutcome(unauthorized, channelID), "401 #5 stays fatal")
}

// TestClassifyChannelOutcome_UnauthorizedRunResets covers the counterpart: any
// non-401 result clears the run, so a flapping 401 never escalates.
func TestClassifyChannelOutcome_UnauthorizedRunResets(t *testing.T) {
	resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 5)

	unauthorized := upstreamError("authentication_error", 401)

	t.Run("success clears the run", func(t *testing.T) {
		const channelID = 7300
		for range 5 {
			assert.Equal(t, OutcomeNeutral, ClassifyChannelOutcome(unauthorized, channelID))
			require.Equal(t, OutcomeSuccess, ClassifyChannelOutcome(nil, channelID))
		}
	})

	t.Run("a non-401 failure also clears the run", func(t *testing.T) {
		const channelID = 7301
		serverError := upstreamError("upstream_error", 500)

		require.Equal(t, OutcomeNeutral, ClassifyChannelOutcome(unauthorized, channelID))
		require.Equal(t, OutcomeNeutral, ClassifyChannelOutcome(unauthorized, channelID))
		require.Equal(t, OutcomeFatal, ClassifyChannelOutcome(serverError, channelID))

		// Run was cleared by the 500, so the next two 401s are neutral again.
		assert.Equal(t, OutcomeNeutral, ClassifyChannelOutcome(unauthorized, channelID))
		assert.Equal(t, OutcomeNeutral, ClassifyChannelOutcome(unauthorized, channelID))
	})
}

// TestClassifyChannelOutcome_UnauthorizedRunIsPerChannel ensures one channel's
// 401 streak cannot escalate another channel.
func TestClassifyChannelOutcome_UnauthorizedRunIsPerChannel(t *testing.T) {
	resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 5)

	const channelA, channelB = 7400, 7401
	unauthorized := upstreamError("authentication_error", 401)

	require.Equal(t, OutcomeNeutral, ClassifyChannelOutcome(unauthorized, channelA))
	require.Equal(t, OutcomeNeutral, ClassifyChannelOutcome(unauthorized, channelA))

	// Channel B is on its first 401 despite channel A sitting at two.
	assert.Equal(t, OutcomeNeutral, ClassifyChannelOutcome(unauthorized, channelB))
	// And channel A still escalates on its own third.
	assert.Equal(t, OutcomeFatal, ClassifyChannelOutcome(unauthorized, channelA))
}

// TestClassifyChannelOutcome_IgnoresKillSwitch: classification drives
// request-level exclusion, which is routing behaviour rather than health scoring,
// so it must keep working when the kill switch is off.
func TestClassifyChannelOutcome_IgnoresKillSwitch(t *testing.T) {
	resetHealthManager()
	setTestConfig(false, 0.3, 0.05, 5)

	const channelID = 7500
	unauthorized := upstreamError("authentication_error", 401)

	assert.Equal(t, OutcomeFatal, ClassifyChannelOutcome(upstreamError("upstream_error", 500), channelID+1))
	assert.Equal(t, OutcomeNeutral, ClassifyChannelOutcome(unauthorized, channelID))
	assert.Equal(t, OutcomeNeutral, ClassifyChannelOutcome(unauthorized, channelID))
	assert.Equal(t, OutcomeFatal, ClassifyChannelOutcome(unauthorized, channelID),
		"the 401 run still escalates while health scoring is disabled")
}

// TestChannelOutcome_Predicates is the truth table for the two routing predicates.
func TestChannelOutcome_Predicates(t *testing.T) {
	cases := []struct {
		outcome        ChannelOutcome
		affectsHealth  bool
		excludeChannel bool
	}{
		{OutcomeSuccess, true, false},
		{OutcomeFatal, true, true},
		{OutcomeThrottled, true, true},
		{OutcomeNeutral, false, false},
	}

	for _, tc := range cases {
		assert.Equal(t, tc.affectsHealth, tc.outcome.AffectsHealth(), "AffectsHealth for %d", tc.outcome)
		assert.Equal(t, tc.excludeChannel, tc.outcome.ExcludesChannel(), "ExcludesChannel for %d", tc.outcome)
	}
}

// TestRecordChannelOutcome_ThrottleConvergesToPartialDerate is the core of the 429
// decision. A permanently throttled channel must settle at a visible derate, not
// collapse to the floor: it is busy, not broken, and starving it would remove
// capacity exactly when it is scarcest.
func TestRecordChannelOutcome_ThrottleConvergesToPartialDerate(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)

	const channelID = 7600
	for range 400 {
		mgr.RecordChannelOutcome(channelID, OutcomeThrottled)
	}

	assert.InDelta(t, throttledObservation, mgr.GetScore(channelID), 1e-6,
		"sustained 429s converge on the throttle observation, roughly a 30%% derate")
	assert.Greater(t, mgr.GetScore(channelID), 0.5, "and stay well clear of the MinScore floor")
}

// TestRecordChannelOutcome_FatalCollapsesToFloor is the contrast case: a genuinely
// broken channel does bottom out.
func TestRecordChannelOutcome_FatalCollapsesToFloor(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)

	const channelID = 7700
	for range 400 {
		mgr.RecordChannelOutcome(channelID, OutcomeFatal)
	}

	assert.InDelta(t, 0.05, mgr.GetScore(channelID), 1e-9, "sustained fatal failures reach MinScore")
	assert.Less(t, mgr.GetScore(channelID), throttledObservation,
		"a broken channel must end up scored below a merely throttled one")
}

// TestRecordChannelOutcome_ThrottleRecoversQuickly: because throttling never drives
// the score far down, recovery is fast once the upstream stops rate limiting.
func TestRecordChannelOutcome_ThrottleRecoversQuickly(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)

	const channelID = 7800
	for range 400 {
		mgr.RecordChannelOutcome(channelID, OutcomeThrottled)
	}
	require.InDelta(t, throttledObservation, mgr.GetScore(channelID), 1e-6)

	successes := 0
	for mgr.GetScore(channelID) < 0.99 && successes < 50 {
		mgr.RecordChannelOutcome(channelID, OutcomeSuccess)
		successes++
	}

	assert.LessOrEqual(t, successes, 10, "recovery to >=0.99 takes at most ten successes")
	assert.GreaterOrEqual(t, mgr.GetScore(channelID), 0.99)
}

// TestRecordChannelOutcome_NeutralIsInert is the key counter-example proving that
// "penalise every failure" would be wrong. A channel whose only failures are
// caller-side 400s must keep full health, and must not even consume its
// MinRequests warm-up budget.
func TestRecordChannelOutcome_NeutralIsInert(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 5)

	const channelID = 7900
	for range 500 {
		mgr.RecordChannelOutcome(channelID, OutcomeNeutral)
	}
	require.Equal(t, 1.0, mgr.GetScore(channelID), "neutral outcomes never move the score")

	// Asserting the score alone is not enough: routing consumes EffectiveWeight,
	// and an earlier revision left a neutral-only channel pinned at the start of
	// its warm-up ramp, derating it to 1/MinRequests of its configured weight
	// indefinitely while the score still read 1.0.
	assert.InDelta(t, 10.0, mgr.EffectiveWeight(channelID, 10), 1e-9,
		"a channel whose only failures are caller-side must keep its full routing weight")

	// requestCount must also be untouched, otherwise 500 neutral results would have
	// silently burned through the MinRequests guard. Five fatal results still sit
	// inside the guard, so the score stays at full health; the sixth moves it.
	for range 5 {
		mgr.RecordChannelOutcome(channelID, OutcomeFatal)
	}
	assert.Equal(t, 1.0, mgr.GetScore(channelID),
		"the MinRequests guard is still intact, so neutral results did not consume it")

	mgr.RecordChannelOutcome(channelID, OutcomeFatal)
	assert.Less(t, mgr.GetScore(channelID), 1.0, "the sixth scored failure finally lowers the score")
}

// TestRecordChannelOutcome_RespectsKillSwitch: health scoring stays gated.
func TestRecordChannelOutcome_RespectsKillSwitch(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(false, 0.3, 0.05, 0)

	const channelID = 8000
	for range 100 {
		mgr.RecordChannelOutcome(channelID, OutcomeFatal)
	}

	assert.Equal(t, defaultScore, mgr.GetScore(channelID),
		"with the kill switch off the score must not move")
}

// TestChannelOutcome_ConcurrentAccess guards the shared 401 counter and score map
// against data races. Run under -race.
func TestChannelOutcome_ConcurrentAccess(t *testing.T) {
	mgr := resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 0)

	errs := []*types.NewAPIError{
		nil,
		upstreamError("authentication_error", 401),
		upstreamError("upstream_error", 500),
		upstreamError("rate_limit_exceeded", 429),
		upstreamError("invalid_request_error", 400),
	}

	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			channelID := 8100 + worker%3
			for i := range 200 {
				outcome := ClassifyChannelOutcome(errs[i%len(errs)], channelID)
				mgr.RecordChannelOutcome(channelID, outcome)
			}
		}(worker)
	}
	wg.Wait()

	for offset := range 3 {
		score := mgr.GetScore(8100 + offset)
		assert.GreaterOrEqual(t, score, 0.05, "score stays at or above MinScore")
		assert.LessOrEqual(t, score, 1.0, "score never exceeds full health")
	}
}
