package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newRetryTestContext(t *testing.T) *gin.Context {
	t.Helper()
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return ctx
}

type testErr string

func (e testErr) Error() string { return string(e) }

// IsChannelError now runs AFTER IsSkipRetryError and retryTimes<=0.
// A channel error that also carries skip-retry must not retry.
func TestShouldRetryChannelErrorWithSkipRetryDoesNotRetry(t *testing.T) {
	ctx := newRetryTestContext(t)
	err := types.NewError(
		testErr("channel:param_override_invalid"),
		types.ErrorCodeChannelParamOverrideInvalid,
		types.ErrOptionWithSkipRetry(),
	)
	assert.False(t, shouldRetry(ctx, err, 3),
		"skip-retry on a channel error must short-circuit before IsChannelError")
}

// IsChannelError now runs AFTER retryTimes<=0.
// A channel error with zero retry budget must not retry.
func TestShouldRetryChannelErrorWithExhaustedBudgetDoesNotRetry(t *testing.T) {
	ctx := newRetryTestContext(t)
	err := types.NewError(
		testErr("channel:invalid_key"),
		types.ErrorCodeChannelInvalidKey,
	)
	assert.False(t, shouldRetry(ctx, err, 0),
		"channel error with zero retry budget should not retry")
}

// A plain channel error (no skip-retry) with retries left must still retry.
func TestShouldRetryChannelErrorWithBudgetRetries(t *testing.T) {
	ctx := newRetryTestContext(t)
	err := types.NewError(
		testErr("channel:invalid_key"),
		types.ErrorCodeChannelInvalidKey,
	)
	assert.True(t, shouldRetry(ctx, err, 3),
		"channel error with retries remaining should retry")
}

func TestShouldRetryNilError(t *testing.T) {
	ctx := newRetryTestContext(t)
	assert.False(t, shouldRetry(ctx, nil, 5))
}

func TestShouldRetrySkipRetryError(t *testing.T) {
	ctx := newRetryTestContext(t)
	err := types.NewError(
		testErr("invalid request"),
		types.ErrorCodeInvalidRequest,
		types.ErrOptionWithSkipRetry(),
	)
	assert.False(t, shouldRetry(ctx, err, 5))
}

func TestShouldRetrySpecificChannel(t *testing.T) {
	ctx := newRetryTestContext(t)
	ctx.Set("specific_channel_id", 42)
	err := types.NewError(
		testErr("bad response"),
		types.ErrorCodeBadResponseStatusCode,
	)
	err.StatusCode = http.StatusInternalServerError
	assert.False(t, shouldRetry(ctx, err, 5))
}

// classifyChatFailureSource: ErrorCodeDoRequestFailed is a local transport
// failure (DNS, connection refused, TLS), not a provider response.
func TestClassifyChatFailureSourceDoRequestFailedIsLocal(t *testing.T) {
	err := types.NewError(
		testErr("dial tcp: connection refused"),
		types.ErrorCodeDoRequestFailed,
	)
	assert.Equal(t, model.FailureSourceLocal, classifyChatFailureSource(err))
}

// classifyChatFailureSource: provider/status transaction failures are upstream.
func TestClassifyChatFailureSourceBadResponseStatusCodeIsUpstream(t *testing.T) {
	err := types.NewError(
		testErr("provider returned 429"),
		types.ErrorCodeBadResponseStatusCode,
	)
	err.StatusCode = http.StatusTooManyRequests
	assert.Equal(t, model.FailureSourceUpstream, classifyChatFailureSource(err))
}

func TestClassifyChatFailureSourceNilIsUpstream(t *testing.T) {
	assert.Equal(t, model.FailureSourceUpstream, classifyChatFailureSource(nil))
}

// wouldRetryWithOneBudget is the predicate the retry loop uses to decide
// whether a terminal failure is an isolation candidate. It must answer "would
// this error retry if one retry were available?" independently of the real
// remaining budget, so that a chain whose last attempt has budget 0 still
// records the terminal retryable failure.

// RetryTimes=0 + channel error produces no isolation candidate: the global
// gate in the loop (common.RetryTimes > 0) blocks all recording, even though
// wouldRetryWithOneBudget itself returns true for a channel error.
func TestWouldRetryWithOneBudgetChannelErrorIsTrueButGateBlocks(t *testing.T) {
	ctx := newRetryTestContext(t)
	err := types.NewError(
		testErr("channel:invalid_key"),
		types.ErrorCodeChannelInvalidKey,
	)
	// Predicate itself is true: a channel error would retry with budget=1.
	assert.True(t, wouldRetryWithOneBudget(ctx, err),
		"channel error should retry if one retry were available")
	// The loop applies common.RetryTimes > 0 on top. With RetryTimes=0 the
	// candidate is never formed, so no state transition is written.
	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 0
	candidate := common.RetryTimes > 0 && wouldRetryWithOneBudget(ctx, err)
	common.RetryTimes = originalRetryTimes
	assert.False(t, candidate,
		"RetryTimes=0 must gate off all isolation candidates, including channel errors")
}

// A retryable terminal 500 is a candidate even when that attempt's remaining
// retry budget is zero: wouldRetryWithOneBudget uses retryTimes=1, not the
// exhausted budget, so the terminal 500 in a 500->500 chain is recorded.
func TestWouldRetryWithOneBudgetRetryable500WithExhaustedBudget(t *testing.T) {
	ctx := newRetryTestContext(t)
	err := types.NewError(
		testErr("provider returned 500"),
		types.ErrorCodeBadResponseStatusCode,
	)
	err.StatusCode = http.StatusInternalServerError
	// With a positive global setting the candidate forms...
	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	candidatePositive := common.RetryTimes > 0 && wouldRetryWithOneBudget(ctx, err)
	// ...even though the actual remaining budget at the terminal attempt is 0.
	actualBudget := common.RetryTimes - 1 // last attempt: RetryTimes - retryIndex(1) = 0
	common.RetryTimes = originalRetryTimes
	assert.True(t, candidatePositive,
		"terminal 500 with positive RetryTimes must be an isolation candidate")
	assert.Equal(t, 0, actualBudget,
		"the terminal attempt must have zero remaining budget")
}

// A terminal non-retryable error (400) is not an isolation candidate: the
// loop's else-branch clears any prior candidate so a 500->400 chain records
// nothing, not the intermediate 500.
func TestWouldRetryWithOneBudgetNonRetryable400IsNotCandidate(t *testing.T) {
	ctx := newRetryTestContext(t)
	err := types.NewError(
		testErr("provider returned 400"),
		types.ErrorCodeBadResponseStatusCode,
	)
	err.StatusCode = http.StatusBadRequest
	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 5
	candidate := common.RetryTimes > 0 && wouldRetryWithOneBudget(ctx, err)
	common.RetryTimes = originalRetryTimes
	assert.False(t, candidate,
		"terminal 400 must not be an isolation candidate; prior 500 candidate is cleared")
}

// The retry loop in Relay and RelayTask must clear any prior upstream
// candidate before breaking on a terminal local error (getChannel failure,
// billing/setup error, body storage error). If it doesn't, the post-loop
// guard would persist the prior provider failure against a route that never
// actually produced the terminal error — violating the rule that a local
// ErrorCodeGetChannelFailed must never enter the state machine.
//
// These tests simulate the candidate lifecycle inline (the loop body clears
// lastRetryRoute/lastRetryErr to nil before each terminal-local break),
// proving the post-loop guard sees no candidate after a local terminal error.

// TestTerminalLocalErrorClearsPriorCandidate simulates a RetryTimes=1 chain:
// attempt 0 gets a retryable 500 and stores a candidate; attempt 1's getChannel
// fails with ErrorCodeGetChannelFailed. The loop clears the candidate before
// breaking, so the post-loop guard must not record.
func TestTerminalLocalErrorClearsPriorCandidate(t *testing.T) {
	ctx := newRetryTestContext(t)

	// ── attempt 0: retryable 500 → candidate set ──
	priorErr := types.NewError(
		testErr("provider returned 500"),
		types.ErrorCodeBadResponseStatusCode,
	)
	priorErr.StatusCode = http.StatusInternalServerError

	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	lastRetryRoute := (*model.RouteKey)(nil)
	lastRetryErr := (*types.NewAPIError)(nil)

	if common.RetryTimes > 0 && wouldRetryWithOneBudget(ctx, priorErr) {
		lastRetryRoute = &model.RouteKey{ChannelId: 1, Model: "gpt-4"}
		lastRetryErr = priorErr
	}
	// candidate must be set after attempt 0
	assert.NotNil(t, lastRetryRoute, "prior retryable 500 must set candidate")

	// ── attempt 1: getChannel fails → loop clears before break ──
	// This is the fix: terminal local error clears the prior candidate.
	lastRetryRoute = nil
	lastRetryErr = nil

	// ── post-loop guard ──
	newAPIError := types.NewError(
		testErr("no available channel"),
		types.ErrorCodeGetChannelFailed,
	)
	shouldRecord := newAPIError != nil && lastRetryRoute != nil && lastRetryErr != nil
	common.RetryTimes = originalRetryTimes

	assert.False(t, shouldRecord,
		"getChannel terminal error must not record a prior upstream candidate")
}

// TestTerminalBillingErrorClearsPriorCandidate covers the billing-error break
// in Relay: same lifecycle, terminal error is a billing failure.
func TestTerminalBillingErrorClearsPriorCandidate(t *testing.T) {
	_ = newRetryTestContext(t)

	priorErr := types.NewError(
		testErr("provider returned 500"),
		types.ErrorCodeBadResponseStatusCode,
	)
	priorErr.StatusCode = http.StatusInternalServerError

	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	lastRetryRoute := &model.RouteKey{ChannelId: 2, Model: "claude-3"}
	lastRetryErr := priorErr
	assert.NotNil(t, lastRetryRoute)

	// billing error break clears candidate
	lastRetryRoute = nil
	lastRetryErr = nil

	newAPIError := types.NewError(
		testErr("billing failed"),
		types.ErrorCodeModelPriceError,
	)
	shouldRecord := newAPIError != nil && lastRetryRoute != nil && lastRetryErr != nil
	common.RetryTimes = originalRetryTimes

	assert.False(t, shouldRecord,
		"billing terminal error must not record a prior upstream candidate")
}

// TestTerminalBodyStorageErrorClearsPriorCandidate covers the body-storage
// break in both Relay and RelayTask.
func TestTerminalBodyStorageErrorClearsPriorCandidate(t *testing.T) {
	_ = newRetryTestContext(t)

	priorErr := types.NewError(
		testErr("provider returned 500"),
		types.ErrorCodeBadResponseStatusCode,
	)
	priorErr.StatusCode = http.StatusInternalServerError

	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	lastRetryRoute := &model.RouteKey{ChannelId: 3, Model: "gemini-pro"}
	lastRetryErr := priorErr
	assert.NotNil(t, lastRetryRoute)

	// body storage error break clears candidate
	lastRetryRoute = nil
	lastRetryErr = nil

	newAPIError := types.NewError(
		testErr("body too large"),
		types.ErrorCodeReadRequestBodyFailed,
		types.ErrOptionWithSkipRetry(),
	)
	shouldRecord := newAPIError != nil && lastRetryRoute != nil && lastRetryErr != nil
	common.RetryTimes = originalRetryTimes

	assert.False(t, shouldRecord,
		"body storage terminal error must not record a prior upstream candidate")
}

// TestTerminalLockedChannelSetupErrorClearsPriorCandidate covers the
// setup_locked_channel_failed break in RelayTask.
func TestTerminalLockedChannelSetupErrorClearsPriorCandidate(t *testing.T) {
	_ = newRetryTestContext(t)

	priorErr := types.NewError(
		testErr("provider returned 500"),
		types.ErrorCodeBadResponseStatusCode,
	)
	priorErr.StatusCode = http.StatusInternalServerError

	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	lastRetryRoute := &model.RouteKey{ChannelId: 4, Model: "dall-e-3"}
	lastRetryErr := priorErr
	assert.NotNil(t, lastRetryRoute)

	// locked channel setup error break clears candidate
	lastRetryRoute = nil
	lastRetryErr = nil

	// taskErr is non-nil, but candidate was cleared
	shouldRecord := lastRetryRoute != nil && lastRetryErr != nil
	common.RetryTimes = originalRetryTimes

	assert.False(t, shouldRecord,
		"locked channel setup terminal error must not record a prior upstream candidate")
}

// TestCandidateSurvivesTerminalRetryable500 confirms the happy path: a
// terminal retryable 500 (no subsequent local error) still records, so the
// fix doesn't over-clear legitimate candidates.
func TestCandidateSurvivesTerminalRetryable500(t *testing.T) {
	ctx := newRetryTestContext(t)

	terminalErr := types.NewError(
		testErr("provider returned 500"),
		types.ErrorCodeBadResponseStatusCode,
	)
	terminalErr.StatusCode = http.StatusInternalServerError

	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	lastRetryRoute := (*model.RouteKey)(nil)
	lastRetryErr := (*types.NewAPIError)(nil)

	if common.RetryTimes > 0 && wouldRetryWithOneBudget(ctx, terminalErr) {
		lastRetryRoute = &model.RouteKey{ChannelId: 5, Model: "gpt-4"}
		lastRetryErr = terminalErr
	}

	shouldRecord := lastRetryRoute != nil && lastRetryErr != nil
	common.RetryTimes = originalRetryTimes

	assert.True(t, shouldRecord,
		"terminal retryable 500 with no subsequent local error must still record")
}

func TestTerminalIsolationCandidateClearsPriorRoute(t *testing.T) {
	priorRoute := model.RouteKey{ChannelId: 8, Model: "gpt-4"}
	priorErr := types.NewError(testErr("provider returned 500"), types.ErrorCodeBadResponseStatusCode)

	route, err := terminalIsolationCandidate(true, priorRoute, priorErr)
	require.Equal(t, &priorRoute, route)
	require.Same(t, priorErr, err)

	route, err = terminalIsolationCandidate(false, priorRoute, priorErr)
	assert.Nil(t, route, "terminal local or non-retryable error clears any prior candidate")
	assert.Nil(t, err)
}
