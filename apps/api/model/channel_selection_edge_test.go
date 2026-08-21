package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// withChannelCacheFixture installs an isolated in-memory channel cache and
// restores the previous globals afterwards, so these tests cannot leak state into
// the rest of the package.
//
// GetRandomSatisfiedChannel reads group2model2channels and channelsIDM under
// channelSyncLock, and only takes the memory-cache path when
// common.MemoryCacheEnabled is true (channel_cache.go:114).
func withChannelCacheFixture(t *testing.T, channels []*Channel, group, modelName string) {
	t.Helper()

	prevGroups := group2model2channels
	prevIDM := channelsIDM
	prevMemoryCache := common.MemoryCacheEnabled
	t.Cleanup(func() {
		channelSyncLock.Lock()
		group2model2channels = prevGroups
		channelsIDM = prevIDM
		channelSyncLock.Unlock()
		common.MemoryCacheEnabled = prevMemoryCache
	})

	ids := make([]int, 0, len(channels))
	idm := make(map[int]*Channel, len(channels))
	for _, ch := range channels {
		ids = append(ids, ch.Id)
		idm[ch.Id] = ch
	}

	channelSyncLock.Lock()
	group2model2channels = map[string]map[string][]int{group: {modelName: ids}}
	channelsIDM = idm
	channelSyncLock.Unlock()
	common.MemoryCacheEnabled = true
}

func testChannel(id int, weight uint, priority int64) *Channel {
	w, p := weight, priority
	return &Channel{Id: id, Weight: &w, Priority: &p}
}

// TestSingleChannelShortCircuitIgnoresWeight pins a deliberate asymmetry: with a
// single candidate, GetRandomSatisfiedChannel returns early without consulting
// weight. There is nothing to fall back to, so selecting it is correct — but it
// means weight=0 behaves differently here than in the multi-channel path, where
// routingBaseWeight maps it to 1. Locking this down so a later refactor does not
// silently start dropping single-channel groups.
func TestSingleChannelShortCircuitIgnoresWeight(t *testing.T) {
	const group, modelName = "edge-group", "edge-model"

	// weight=0 would be the least attractive channel possible in the weighted path.
	only := testChannel(9101, 0, 7)
	withChannelCacheFixture(t, []*Channel{only}, group, modelName)
	ClearRouteHealthCache()
	t.Cleanup(ClearRouteHealthCache)

	got, err := GetRandomSatisfiedChannel(group, modelName, 0, "", nil)
	require.NoError(t, err)
	require.NotNil(t, got, "a single candidate must still be selected")
	assert.Equal(t, only.Id, got.Id)
}

// TestSingleChannelShortCircuitRespectsExcludeSet: the short circuit must not
// bypass request-level exclusion, otherwise a channel that just failed would be
// handed back to the same request.
func TestSingleChannelShortCircuitRespectsExcludeSet(t *testing.T) {
	const group, modelName = "edge-group", "edge-model"

	only := testChannel(9102, 10, 7)
	withChannelCacheFixture(t, []*Channel{only}, group, modelName)
	ClearRouteHealthCache()
	t.Cleanup(ClearRouteHealthCache)

	got, err := GetRandomSatisfiedChannel(group, modelName, 0, "", map[int]bool{only.Id: true})
	require.NoError(t, err)
	assert.Nil(t, got, "the only channel was excluded, so selection must yield nothing")
}

// TestRetryDescendsPriorityTiers pins the priority semantics that retry drives.
// sortedUniquePriorities is sorted with sort.Reverse (channel_cache.go:154), so
// index 0 is the NUMERICALLY HIGHEST priority and retry walks downward. The clamp
// at channel_cache.go:157 pegs an over-large retry to the lowest tier.
func TestRetryDescendsPriorityTiers(t *testing.T) {
	const group, modelName = "edge-group", "edge-model"

	// One channel per tier so the selected tier is unambiguous.
	channels := []*Channel{
		testChannel(9201, 10, 100),
		testChannel(9202, 10, 50),
		testChannel(9203, 10, 10),
	}
	withChannelCacheFixture(t, channels, group, modelName)
	ClearRouteHealthCache()
	t.Cleanup(ClearRouteHealthCache)

	cases := []struct {
		retry         int
		wantChannelID int
		wantPriority  int64
		explanation   string
	}{
		{0, 9201, 100, "retry 0 takes the highest priority tier"},
		{1, 9202, 50, "retry 1 descends one tier"},
		{2, 9203, 10, "retry 2 reaches the lowest tier"},
		{3, 9203, 10, "retry beyond the tier count is clamped to the lowest"},
		{99, 9203, 10, "a far larger retry is clamped identically"},
	}

	for _, tc := range cases {
		t.Run(tc.explanation, func(t *testing.T) {
			got, err := GetRandomSatisfiedChannel(group, modelName, tc.retry, "", nil)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, tc.wantChannelID, got.Id)
			assert.Equal(t, tc.wantPriority, got.GetPriority())
		})
	}
}

// TestIsolationActsWithinPriorityTierOnly documents that route isolation is
// tier-local: an isolated route loses every pick to its peers at the same
// priority, but isolation never promotes or demotes a channel across tiers.
// Cross-tier movement is driven solely by retry.
func TestIsolationActsWithinPriorityTierOnly(t *testing.T) {
	const group, modelName = "edge-group", "edge-model"

	healthy := testChannel(9301, 10, 100)
	isolated := testChannel(9302, 10, 100)
	lowerTier := testChannel(9303, 10, 10)
	withChannelCacheFixture(t, []*Channel{healthy, isolated, lowerTier}, group, modelName)

	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())

	// The selectors read the real clock, so the isolation window must be live.
	require.NoError(t, RecordRetryableFailure(RouteKey{ChannelId: isolated.Id, Model: modelName}, "bad_response", time.Now()))

	// retry=0 stays inside the top tier, and the isolated peer must never win.
	counts := map[int]int{}
	for range 400 {
		got, err := GetRandomSatisfiedChannel(group, modelName, 0, "", nil)
		require.NoError(t, err)
		require.NotNil(t, got)
		counts[got.Id]++
	}

	assert.Zero(t, counts[lowerTier.Id],
		"a lower tier must never be reached while retry is 0, regardless of health")
	assert.Zero(t, counts[isolated.Id], "an isolated route is excluded, not merely derated")
	assert.Equal(t, 400, counts[healthy.Id], "the healthy peer absorbs the whole tier's traffic")

	// retry=1 still descends to the lower tier even though the top tier holds a
	// perfectly healthy channel: health never overrides the tier walk.
	got, err := GetRandomSatisfiedChannel(group, modelName, 1, "", nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, lowerTier.Id, got.Id)
}
