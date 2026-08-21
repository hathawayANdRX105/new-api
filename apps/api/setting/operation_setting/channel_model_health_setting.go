package operation_setting

import (
	"fmt"
	"strconv"
	"sync/atomic"
)

// ChannelModelHealthSetting controls retry-driven channel×model isolation.
type ChannelModelHealthSetting struct {
	CalmFastBase            int `json:"calm_fast_base"`
	CalmFastInterval        int `json:"calm_fast_interval"`
	CalmSlowBase            int `json:"calm_slow_base"`
	CalmSlowInterval        int `json:"calm_slow_interval"`
	DormantBase             int `json:"dormant_base"`
	DormantInterval         int `json:"dormant_interval"`
	DormantMaxBase          int `json:"dormant_max_base"`
	DormantDisableThreshold int `json:"dormant_disable_threshold"`
}

func DefaultChannelModelHealthSetting() *ChannelModelHealthSetting {
	return &ChannelModelHealthSetting{CalmFastBase: 3, CalmFastInterval: 3, CalmSlowBase: 20, CalmSlowInterval: 20, DormantBase: 120, DormantInterval: 120, DormantMaxBase: 360}
}

var channelModelHealthSetting atomic.Pointer[ChannelModelHealthSetting]

func init() { channelModelHealthSetting.Store(DefaultChannelModelHealthSetting()) }
func GetChannelModelHealthSetting() *ChannelModelHealthSetting {
	return channelModelHealthSetting.Load()
}

var channelModelHealthKeys = map[string]struct{}{
	"CalmFastBase": {}, "CalmFastInterval": {}, "CalmSlowBase": {}, "CalmSlowInterval": {},
	"DormantBase": {}, "DormantInterval": {}, "DormantMaxBase": {}, "DormantDisableThreshold": {},
}

func IsChannelModelHealthOptionKey(key string) bool { _, ok := channelModelHealthKeys[key]; return ok }

func ValidateChannelModelHealthSettingValue(key, value string) error {
	if !IsChannelModelHealthOptionKey(key) {
		return fmt.Errorf("unknown channel model health option %q", key)
	}
	v, err := strconv.Atoi(value)
	if err != nil || v < 0 {
		return fmt.Errorf("%s must be a non-negative integer", key)
	}
	return nil
}

func UpdateChannelModelHealthSettingValue(key, value string) error {
	if err := ValidateChannelModelHealthSettingValue(key, value); err != nil {
		return err
	}
	v, _ := strconv.Atoi(value)
	old := channelModelHealthSetting.Load()
	next := *old
	switch key {
	case "CalmFastBase":
		next.CalmFastBase = v
	case "CalmFastInterval":
		next.CalmFastInterval = v
	case "CalmSlowBase":
		next.CalmSlowBase = v
	case "CalmSlowInterval":
		next.CalmSlowInterval = v
	case "DormantBase":
		next.DormantBase = v
	case "DormantInterval":
		next.DormantInterval = v
	case "DormantMaxBase":
		next.DormantMaxBase = v
	case "DormantDisableThreshold":
		next.DormantDisableThreshold = v
	}
	channelModelHealthSetting.Store(&next)
	return nil
}
