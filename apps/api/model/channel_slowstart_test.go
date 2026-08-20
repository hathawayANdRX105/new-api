package model

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/QuantumNous/new-api/relaykit/types"
)

// TestSlowStartFactorTable exercises slowStartFactor directly. EffectiveWeight
// covers it indirectly, but the boundary conditions (window disabled, ramp
// abandoned, window completed) are easier to pin exhaustively here.
func TestSlowStartFactorTable(t *testing.T) {
	cases := []struct {
		name         string
		requestCount int
		rampExited   bool
		minRequests  int
		want         float64
	}{
		{"window disabled by zero", 0, false, 0, 1.0},
		{"window disabled by negative", 3, false, -1, 1.0},
		{"no outcome observed yet is not derated", 0, false, 5, 1.0},
		{"first observed outcome", 1, false, 5, 0.2},
		{"second", 2, false, 5, 0.4},
		{"third", 3, false, 5, 0.6},
		{"fourth", 4, false, 5, 0.8},
		{"window completed", 5, false, 5, 1.0},
		{"past the window", 9, false, 5, 1.0},
		{"ramp abandoned mid-window", 1, true, 5, 1.0},
		{"single-request window completes immediately", 1, false, 1, 1.0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &channelHealthState{requestCount: tc.requestCount, rampExited: tc.rampExited}
			assert.InDelta(t, tc.want, slowStartFactor(state, tc.minRequests), 1e-9)
		})
	}
}

// TestSlowStartFactorNeverExceedsFullWeight guards the invariant that matters for
// routing: the ramp may only ever reduce a channel's weight, never inflate it.
func TestSlowStartFactorNeverExceedsFullWeight(t *testing.T) {
	for _, minRequests := range []int{1, 2, 5, 10, 50} {
		for count := 0; count <= minRequests+2; count++ {
			f := slowStartFactor(&channelHealthState{requestCount: count}, minRequests)
			assert.Greater(t, f, 0.0, "minRequests=%d count=%d", minRequests, count)
			assert.LessOrEqual(t, f, 1.0, "minRequests=%d count=%d", minRequests, count)
		}
	}
}

// TestRelayWiringContract pins the decision table that controller/relay.go relies
// on when it calls ClassifyChannelOutcome and then branches on AffectsHealth and
// ExcludesChannel. relay.go itself has no unit-test harness, so this locks the
// contract it consumes: exactly which upstream results cost a channel its health
// and which merely divert the current request.
func TestRelayWiringContract(t *testing.T) {
	resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 5)

	cases := []struct {
		name       string
		status     int
		code       types.ErrorCode
		wantHealth bool
		wantSkip   bool
	}{
		{"success", 0, "", true, false},
		{"upstream 500 is the channel's fault", 500, "upstream_error", true, true},
		{"gateway timeout", 504, "upstream_error", true, true},
		{"empty body is unusable", 200, types.ErrorCodeBadResponseBody, true, true},
		{"local channel error", 401, types.ErrorCodeChannelInvalidKey, true, true},
		{"429 diverts but is not a fault", 429, "rate_limit_exceeded", true, true},
		{"400 is the caller's fault", 400, "invalid_request_error", false, false},
		{"403 says nothing about the channel", 403, "permission_error", false, false},
		{"404 wrong model", 404, "not_found", false, false},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err *types.NewAPIError
			if tc.status != 0 {
				err = types.NewErrorWithStatusCode(errors.New("x"), tc.code, tc.status)
			}
			// Distinct channel per case so the 401 run counter cannot leak across rows.
			outcome := ClassifyChannelOutcome(err, 8300+i)
			assert.Equal(t, tc.wantHealth, outcome.AffectsHealth(), "AffectsHealth")
			assert.Equal(t, tc.wantSkip, outcome.ExcludesChannel(), "ExcludesChannel")
		})
	}
}
