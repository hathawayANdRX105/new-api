package model

import (
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetPressure clears the pressure map so cases do not see counters left
// behind by earlier tests or by the package-level init.
func resetPressure() {
	pressureLock.Lock()
	pressureIDM = map[string]*modelPressure{}
	pressureLock.Unlock()
}

// setPressure installs a known total/healthy pair for a model.
func setPressure(model string, total, healthy int) {
	pressureLock.Lock()
	pressureIDM[model] = &modelPressure{total: total, healthy: healthy}
	pressureLock.Unlock()
}

func TestModelPressureLevel_ThreeTierBoundaries(t *testing.T) {
	resetPressure()
	// Default thresholds: EmergencyThreshold=20, WarningThreshold=50.
	// total=10 → healthy counts at the boundaries:
	//   1/10 = 10% → Emergency; 2/10 = 20% → Warning; 5/10 = 50% → Normal.
	cases := []struct {
		name    string
		healthy int
		want    PressureLevel
	}{
		{"1/10 = 10% → emergency", 1, PressureEmergency},
		{"2/10 = 20% → warning (not < 20)", 2, PressureWarning},
		{"5/10 = 50% → normal (not < 50)", 5, PressureNormal},
		{"4/10 = 40% → warning", 4, PressureWarning},
		{"10/10 = 100% → normal", 10, PressureNormal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setPressure("test-model", 10, tc.healthy)
			assert.Equal(t, tc.want, modelPressureLevel("test-model"))
		})
	}
}

func TestModelPressureLevel_TotalZeroFallsToNormal(t *testing.T) {
	resetPressure()
	setPressure("zero-model", 0, 0)
	assert.Equal(t, PressureNormal, modelPressureLevel("zero-model"))

	resetPressure()
	// Unknown model (no entry) also normal.
	assert.Equal(t, PressureNormal, modelPressureLevel("nonexistent"))
}

func TestDecayStep_SwitchesWithPressure(t *testing.T) {
	resetPressure()
	// Normal pressure → NormalDecayStep (default 1).
	setPressure("ok", 10, 10)
	assert.Equal(t, 1, decayStep("ok"))

	// Warning pressure → AcceleratedDecayStep (default 2).
	setPressure("warn", 10, 4) // 40% < 50%
	assert.Equal(t, 2, decayStep("warn"))

	// Emergency pressure → NormalDecayStep (not warning, so default 1).
	setPressure("emerg", 10, 1) // 10% < 20%
	assert.Equal(t, 1, decayStep("emerg"))
}

func TestPressureOnStateChange_CrossingBoundary(t *testing.T) {
	resetPressure()
	setPressure("m", 10, 10)
	key := RouteKey{ChannelId: 1, KeyIndex: 0, Model: "m"}

	// healthy → calm: healthy decrements.
	pressureOnStateChange(key, HealthHealthy, HealthCalm)
	assert.Equal(t, 9, pressureIDM["m"].healthy)

	// calm → dormant: same non-healthy side, no change.
	pressureOnStateChange(key, HealthCalm, HealthDormant)
	assert.Equal(t, 9, pressureIDM["m"].healthy)

	// dormant → healthy: healthy increments.
	pressureOnStateChange(key, HealthDormant, HealthHealthy)
	assert.Equal(t, 10, pressureIDM["m"].healthy)
}

func TestPressureOnStateChange_FloorAtZero(t *testing.T) {
	resetPressure()
	setPressure("floor", 5, 0)
	key := RouteKey{ChannelId: 1, KeyIndex: 0, Model: "floor"}

	// healthy(0) → calm should not go negative.
	pressureOnStateChange(key, HealthHealthy, HealthCalm)
	pressureLock.RLock()
	p := pressureIDM["floor"]
	pressureLock.RUnlock()
	assert.Equal(t, 0, p.healthy)
}

func TestPressureOnRemove_DecrementsTotalAndHealthy(t *testing.T) {
	resetPressure()
	setPressure("rm", 5, 5)
	key := RouteKey{ChannelId: 7, KeyIndex: 0, Model: "rm"}

	pressureOnRemove(key)
	pressureLock.RLock()
	p := pressureIDM["rm"]
	pressureLock.RUnlock()
	assert.Equal(t, 4, p.total)
	assert.Equal(t, 4, p.healthy)
}

func TestPressureOnRemove_FlooredAtZero(t *testing.T) {
	resetPressure()
	setPressure("rm0", 1, 1)
	key := RouteKey{ChannelId: 9, KeyIndex: 0, Model: "rm0"}

	pressureOnRemove(key)
	pressureLock.RLock()
	p := pressureIDM["rm0"]
	pressureLock.RUnlock()
	assert.Equal(t, 0, p.total)
	assert.Equal(t, 0, p.healthy)
}

func TestDefaultChannelModelHealthSetting_NewFields(t *testing.T) {
	s := operation_setting.DefaultChannelModelHealthSetting()
	require.NotNil(t, s)
	assert.Equal(t, 20, s.EmergencyThreshold)
	assert.Equal(t, 50, s.WarningThreshold)
	assert.Equal(t, 2, s.AcceleratedDecayStep)
	assert.Equal(t, 1, s.NormalDecayStep)
	assert.True(t, s.KeyProbeEnabled)
	assert.Equal(t, 3, s.DormantDisableThreshold) // changed from 0
}

func TestValidateChannelModelHealthSettingValue_NewKeys(t *testing.T) {
	// EmergencyThreshold / WarningThreshold: 0–100.
	for _, key := range []string{"EmergencyThreshold", "WarningThreshold"} {
		assert.NoError(t, operation_setting.ValidateChannelModelHealthSettingValue(key, "0"))
		assert.NoError(t, operation_setting.ValidateChannelModelHealthSettingValue(key, "50"))
		assert.NoError(t, operation_setting.ValidateChannelModelHealthSettingValue(key, "100"))
		assert.Error(t, operation_setting.ValidateChannelModelHealthSettingValue(key, "101"))
		assert.Error(t, operation_setting.ValidateChannelModelHealthSettingValue(key, "-1"))
		assert.Error(t, operation_setting.ValidateChannelModelHealthSettingValue(key, "abc"))
	}

	// AcceleratedDecayStep / NormalDecayStep: >= 1.
	for _, key := range []string{"AcceleratedDecayStep", "NormalDecayStep"} {
		assert.NoError(t, operation_setting.ValidateChannelModelHealthSettingValue(key, "1"))
		assert.NoError(t, operation_setting.ValidateChannelModelHealthSettingValue(key, "5"))
		assert.Error(t, operation_setting.ValidateChannelModelHealthSettingValue(key, "0"))
		assert.Error(t, operation_setting.ValidateChannelModelHealthSettingValue(key, "-1"))
		assert.Error(t, operation_setting.ValidateChannelModelHealthSettingValue(key, "1.5"))
	}

	// KeyProbeEnabled: only "true"/"false".
	assert.NoError(t, operation_setting.ValidateChannelModelHealthSettingValue("KeyProbeEnabled", "true"))
	assert.NoError(t, operation_setting.ValidateChannelModelHealthSettingValue("KeyProbeEnabled", "false"))
	assert.Error(t, operation_setting.ValidateChannelModelHealthSettingValue("KeyProbeEnabled", "1"))
	assert.Error(t, operation_setting.ValidateChannelModelHealthSettingValue("KeyProbeEnabled", "0"))
	assert.Error(t, operation_setting.ValidateChannelModelHealthSettingValue("KeyProbeEnabled", "yes"))
}

func TestUpdateChannelModelHealthSettingValue_KeyProbeEnabled(t *testing.T) {
	orig := operation_setting.GetChannelModelHealthSetting()
	t.Cleanup(func() { operation_setting.RestoreChannelModelHealthSetting(orig) })

	require.NoError(t, operation_setting.UpdateChannelModelHealthSettingValue("KeyProbeEnabled", "false"))
	assert.False(t, operation_setting.GetChannelModelHealthSetting().KeyProbeEnabled)

	require.NoError(t, operation_setting.UpdateChannelModelHealthSettingValue("KeyProbeEnabled", "true"))
	assert.True(t, operation_setting.GetChannelModelHealthSetting().KeyProbeEnabled)

	// Other fields unchanged.
	updated := operation_setting.GetChannelModelHealthSetting()
	assert.Equal(t, orig.EmergencyThreshold, updated.EmergencyThreshold)
}

func TestUpdateChannelModelHealthSettingValue_IntegerKeys(t *testing.T) {
	orig := operation_setting.GetChannelModelHealthSetting()
	t.Cleanup(func() { operation_setting.RestoreChannelModelHealthSetting(orig) })

	intKeys := map[string]int{
		"EmergencyThreshold":   15,
		"WarningThreshold":     45,
		"AcceleratedDecayStep": 3,
		"NormalDecayStep":      2,
	}
	for key, val := range intKeys {
		require.NoError(t, operation_setting.UpdateChannelModelHealthSettingValue(key, strconv.Itoa(val)))
	}
	updated := operation_setting.GetChannelModelHealthSetting()
	assert.Equal(t, 15, updated.EmergencyThreshold)
	assert.Equal(t, 45, updated.WarningThreshold)
	assert.Equal(t, 3, updated.AcceleratedDecayStep)
	assert.Equal(t, 2, updated.NormalDecayStep)
}
