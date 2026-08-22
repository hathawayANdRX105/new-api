package operation_setting

import (
	"fmt"
	"strconv"
	"sync/atomic"
)

// ChannelModelHealthSetting controls retry-driven channel×model isolation.
type ChannelModelHealthSetting struct {
	CalmFastBase             int `json:"calm_fast_base"`
	CalmFastInterval         int `json:"calm_fast_interval"`
	CalmSlowBase             int `json:"calm_slow_base"`
	CalmSlowInterval         int `json:"calm_slow_interval"`
	DormantBase              int `json:"dormant_base"`
	DormantInterval          int `json:"dormant_interval"`
	DormantMaxBase           int `json:"dormant_max_base"`
	DormantDisableThreshold  int `json:"dormant_disable_threshold"`
	LocalFailureThreshold    int `json:"local_failure_threshold"`
	UpstreamFailureThreshold int `json:"upstream_failure_threshold"`
	// Wave C soft deprecation: calm routes keep competing but at a reduced
	// weight; dormant routes compete at an even lower weight. Values are
	// percentages 0–100 (100 = full weight, 0 = effectively excluded).
	CalmWeightScale    int `json:"calm_weight_scale"`
	DormantWeightScale int `json:"dormant_weight_scale"`
	// Wave E pool-pressure thresholds: emergency/warning availability percentages.
	EmergencyThreshold   int  `json:"emergency_threshold"`
	WarningThreshold     int  `json:"warning_threshold"`
	AcceleratedDecayStep int  `json:"accelerated_decay_step"`
	NormalDecayStep      int  `json:"normal_decay_step"`
	KeyProbeEnabled      bool `json:"key_probe_enabled"`
}

func DefaultChannelModelHealthSetting() *ChannelModelHealthSetting {
	return &ChannelModelHealthSetting{
		CalmFastBase: 3, CalmFastInterval: 3, CalmSlowBase: 20, CalmSlowInterval: 20,
		DormantBase: 120, DormantInterval: 120, DormantMaxBase: 360,
		DormantDisableThreshold: 3,
		LocalFailureThreshold:   1, UpstreamFailureThreshold: 1,
		CalmWeightScale: 50, DormantWeightScale: 10,
		EmergencyThreshold: 20, WarningThreshold: 50,
		AcceleratedDecayStep: 2, NormalDecayStep: 1,
		KeyProbeEnabled: true,
	}
}

var channelModelHealthSetting atomic.Pointer[ChannelModelHealthSetting]

func init() { channelModelHealthSetting.Store(DefaultChannelModelHealthSetting()) }
func GetChannelModelHealthSetting() *ChannelModelHealthSetting {
	return channelModelHealthSetting.Load()
}

// RestoreChannelModelHealthSetting atomically replaces the current config.
// Intended for test setup/teardown; callers must not mutate the pointer
// after passing it.
func RestoreChannelModelHealthSetting(s *ChannelModelHealthSetting) {
	channelModelHealthSetting.Store(s)
}

var channelModelHealthKeys = map[string]struct{}{
	"CalmFastBase": {}, "CalmFastInterval": {}, "CalmSlowBase": {}, "CalmSlowInterval": {},
	"DormantBase": {}, "DormantInterval": {}, "DormantMaxBase": {}, "DormantDisableThreshold": {},
	"LocalFailureThreshold": {}, "UpstreamFailureThreshold": {},
	"CalmWeightScale": {}, "DormantWeightScale": {},
	"EmergencyThreshold": {}, "WarningThreshold": {},
	"AcceleratedDecayStep": {}, "NormalDecayStep": {},
	"KeyProbeEnabled": {},
}

func IsChannelModelHealthOptionKey(key string) bool { _, ok := channelModelHealthKeys[key]; return ok }

func ValidateChannelModelHealthSettingValue(key, value string) error {
	if !IsChannelModelHealthOptionKey(key) {
		return fmt.Errorf("unknown channel model health option %q", key)
	}
	// KeyProbeEnabled is a boolean, not an integer — bypass the strconv path.
	if key == "KeyProbeEnabled" {
		if value != "true" && value != "false" {
			return fmt.Errorf("%s must be \"true\" or \"false\"", key)
		}
		return nil
	}
	v, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%s must be an integer", key)
	}
	if key == "LocalFailureThreshold" || key == "UpstreamFailureThreshold" {
		if v < 1 {
			return fmt.Errorf("%s must be a positive integer (>=1)", key)
		}
	} else if key == "CalmWeightScale" || key == "DormantWeightScale" {
		if v < 0 || v > 100 {
			return fmt.Errorf("%s must be a percentage 0-100", key)
		}
	} else if key == "EmergencyThreshold" || key == "WarningThreshold" {
		if v < 0 || v > 100 {
			return fmt.Errorf("%s must be a percentage 0-100", key)
		}
	} else if key == "AcceleratedDecayStep" || key == "NormalDecayStep" {
		if v < 1 {
			return fmt.Errorf("%s must be a positive integer (>=1)", key)
		}
	} else if v < 0 {
		return fmt.Errorf("%s must be a non-negative integer", key)
	}
	return nil
}

func UpdateChannelModelHealthSettingValue(key, value string) error {
	if err := ValidateChannelModelHealthSettingValue(key, value); err != nil {
		return err
	}
	v, _ := strconv.Atoi(value)
	boolVal := value == "true"
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
	case "LocalFailureThreshold":
		next.LocalFailureThreshold = v
	case "UpstreamFailureThreshold":
		next.UpstreamFailureThreshold = v
	case "CalmWeightScale":
		next.CalmWeightScale = v
	case "DormantWeightScale":
		next.DormantWeightScale = v
	case "EmergencyThreshold":
		next.EmergencyThreshold = v
	case "WarningThreshold":
		next.WarningThreshold = v
	case "AcceleratedDecayStep":
		next.AcceleratedDecayStep = v
	case "NormalDecayStep":
		next.NormalDecayStep = v
	case "KeyProbeEnabled":
		next.KeyProbeEnabled = boolVal
	}
	channelModelHealthSetting.Store(&next)
	return nil
}
