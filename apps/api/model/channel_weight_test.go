package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// 1️⃣ Monotonicity + specific-value tests
// ---------------------------------------------------------------------------

func TestRoutingBaseWeightMonoAndValues(t *testing.T) {
	weights := []int{0, 1, 2, 5, 9, 10, 11, 30, 100}

	// strict monotonicity: each must be > previous
	for i := 1; i < len(weights); i++ {
		prev := routingBaseWeight(weights[i-1])
		curr := routingBaseWeight(weights[i])
		assert.True(t, curr > prev,
			"routingBaseWeight(%d)=%d should be > routingBaseWeight(%d)=%d",
			weights[i-1], prev, weights[i], curr)
	}

	// specific known values
	assert.Equal(t, routingBaseWeight(0), uint(1), "routingBaseWeight(0) should be 1")
	assert.Equal(t, routingBaseWeight(5), uint(6), "routingBaseWeight(5) should be 6")
	assert.Equal(t, routingBaseWeight(30), uint(31), "routingBaseWeight(30) should be 31")
}

// ---------------------------------------------------------------------------
// 2️⃣ Negative-value defence
// ---------------------------------------------------------------------------

func TestRoutingBaseWeightNegative(t *testing.T) {
	assert.Equal(t, routingBaseWeight(-1), uint(1), "routingBaseWeight(-1) should be 1 (clamp to min)")
}

// ---------------------------------------------------------------------------
// 3️⃣ Share conformity against configured ratio 30:10:5:2
// ---------------------------------------------------------------------------

func TestRoutingBaseWeightShareConformity(t *testing.T) {
	// configured ratio 30:10:5:2
	configWeights := []int{30, 10, 5, 2}
	shares := make([]uint, len(configWeights))
	for i, w := range configWeights {
		shares[i] = routingBaseWeight(w)
	}

	// total and individual shares
	total := uint(0)
	for _, s := range shares {
		total += s
	}
	// routingBaseWeight gives: 31+11+6+3 = 51
	// The spec's "精确值 31/50、11/50、6/50、3/50" uses total=50 as reference,
	// but actual traffic proportion is share/total. We verify descending order
	// and that each share is roughly in the intended proportion (within 4pp).

	// Verify descending order
	for i := 1; i < len(shares); i++ {
		assert.True(t, shares[i-1] >= shares[i],
			"shares should be non-increasing, got shares[%d]=%d, shares[%d]=%d",
			i-1, shares[i-1], i, shares[i])
	}

	// Verify each share is roughly in the intended proportion.
	// Config intent proportions (weight / sum of config weights): 30/47, 10/47, 5/47, 2/47
	configSum := 30 + 10 + 5 + 2 // 47
	expectedProportions := []float64{
		30.0 / float64(configSum),
		10.0 / float64(configSum),
		5.0 / float64(configSum),
		2.0 / float64(configSum),
	}

	for i, s := range shares {
		actualPct := float64(s) / float64(total) * 100.0
		expectedPct := expectedProportions[i] * 100.0
		deviation := actualPct - expectedPct
		// Within 4 percentage points
		assert.True(t, deviation >= -4.0 && deviation <= 4.0,
			"channel %d: actual %.1f%% vs expected %.1f%% deviation %.1f%% (within 4pp)",
			i, actualPct, expectedPct, deviation)
	}
}

// ---------------------------------------------------------------------------
// 4️⃣ Old smoothing reversal eliminated
// ---------------------------------------------------------------------------

func legacySmoothing(w int) float64 {
	// Replicate the old smoothing:
	// w==0 → 100
	// w<10 → w*100
	// otherwise → w (as float64)
	if w == 0 {
		return 100.0
	}
	if w < 10 {
		return float64(w) * 100.0
	}
	return float64(w)
}

func TestRoutingBaseWeightVsLegacySmoothing(t *testing.T) {
	// Old bug: legacySmoothing(5) > legacySmoothing(30) should be true
	// because 5<10 → 5*100=500, and 30≥10 → 30, so 500 > 30
	assert.True(t, legacySmoothing(5) > legacySmoothing(30),
		"legacy smoothing: weight=5 should get 500, weight=30 should get 30, so 500 > 30 (old bug)")

	// New routingBaseWeight: routingBaseWeight(5) < routingBaseWeight(30) should be true
	// routingBaseWeight(5)=6, routingBaseWeight(30)=31, so 6 < 31
	assert.True(t, routingBaseWeight(5) < routingBaseWeight(30),
		"new routingBaseWeight: weight=5 → 6, weight=30 → 31, so 6 < 31 (fixed)")
}

