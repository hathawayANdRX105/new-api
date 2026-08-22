package operation_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultChannelModelHealthSetting_FailureThresholdsAreOne(t *testing.T) {
	s := DefaultChannelModelHealthSetting()

	require.NotNil(t, s)
	assert.Equal(t, 1, s.LocalFailureThreshold)
	assert.Equal(t, 1, s.UpstreamFailureThreshold)
}

func TestValidateChannelModelHealthSettingValue_FailureThresholds(t *testing.T) {
	thresholdKeys := []string{"LocalFailureThreshold", "UpstreamFailureThreshold"}

	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"accepts one", "1", false},
		{"accepts large positive", "5", false},
		{"rejects zero", "0", true},
		{"rejects negative", "-1", true},
		{"rejects fractional", "1.5", true},
		{"rejects nonnumeric", "abc", true},
	}

	for _, key := range thresholdKeys {
		for _, tc := range cases {
			t.Run(key+"/"+tc.name, func(t *testing.T) {
				err := ValidateChannelModelHealthSettingValue(key, tc.value)
				if tc.wantErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}
			})
		}
	}
}

func TestValidateChannelModelHealthSettingValue_DormantDisableThresholdAllowsZero(t *testing.T) {
	assert.NoError(t, ValidateChannelModelHealthSettingValue("DormantDisableThreshold", "0"))
	assert.NoError(t, ValidateChannelModelHealthSettingValue("DormantDisableThreshold", "10"))
	assert.Error(t, ValidateChannelModelHealthSettingValue("DormantDisableThreshold", "-1"))
	assert.Error(t, ValidateChannelModelHealthSettingValue("DormantDisableThreshold", "1.5"))
	assert.Error(t, ValidateChannelModelHealthSettingValue("DormantDisableThreshold", "abc"))
}

func TestValidateChannelModelHealthSettingValue_UnknownKey(t *testing.T) {
	assert.Error(t, ValidateChannelModelHealthSettingValue("NonexistentKey", "1"))
	assert.Error(t, ValidateChannelModelHealthSettingValue("", "1"))
}

func TestUpdateChannelModelHealthSettingValue_OnlyChangesRequestedThreshold(t *testing.T) {
	orig := GetChannelModelHealthSetting()
	t.Cleanup(func() { channelModelHealthSetting.Store(orig) })

	t.Run("LocalFailureThreshold", func(t *testing.T) {
		require.NoError(t, UpdateChannelModelHealthSettingValue("LocalFailureThreshold", "7"))

		updated := GetChannelModelHealthSetting()
		assert.Equal(t, 7, updated.LocalFailureThreshold)
		assert.Equal(t, orig.UpstreamFailureThreshold, updated.UpstreamFailureThreshold)
	})

	t.Run("UpstreamFailureThreshold", func(t *testing.T) {
		channelModelHealthSetting.Store(orig)
		require.NoError(t, UpdateChannelModelHealthSettingValue("UpstreamFailureThreshold", "9"))

		updated := GetChannelModelHealthSetting()
		assert.Equal(t, 9, updated.UpstreamFailureThreshold)
		assert.Equal(t, orig.LocalFailureThreshold, updated.LocalFailureThreshold)
	})
}
