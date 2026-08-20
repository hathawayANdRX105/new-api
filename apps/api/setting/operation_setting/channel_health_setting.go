package operation_setting

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync/atomic"
)

// ChannelHealthSetting configures the EWMA-based channel health scoring.
//
// When Enabled is false, the system falls back to baseline behavior (pure
// weighted random without health adjustment). This serves as a kill switch:
// toggling it off instantly restores baseline, with no restart required.
type ChannelHealthSetting struct {
	Enabled     bool    `json:"enabled"`
	Alpha       float64 `json:"alpha"`        // EWMA smoothing factor (0-1), default 0.3
	MinScore    float64 `json:"min_score"`    // Floor: minimum health score, default 0.05
	MinRequests int     `json:"min_requests"` // Min requests before EWMA is trusted, default 5

	// Cooldown ejection: after CooldownThreshold consecutive fatal/throttled
	// outcomes a channel is ejected for a sliding duration that starts at
	// CooldownBaseSeconds and approaches CooldownMaxSeconds with repeated
	// activations. CooldownMaxEjectionPercent caps how many simultaneously
	// cooling channels in a tier may be truly zeroed; the rest stay selectable
	// as a degraded availability fallback. CooldownAlpha is an independent
	// sliding-duration factor (0-1), not the EWMA Alpha above.
	CooldownThreshold          int     `json:"cooldown_threshold"`
	CooldownBaseSeconds        int     `json:"cooldown_base_seconds"`
	CooldownMaxSeconds         int     `json:"cooldown_max_seconds"`
	CooldownMaxEjectionPercent int     `json:"cooldown_max_ejection_percent"`
	CooldownAlpha              float64 `json:"cooldown_alpha"`

	// CooldownDisableStreak is how many cooldown activations one channel+model
	// pair may accumulate before that model is disabled on that channel.
	// Cooldown alone never terminates: a permanently dead upstream just cycles
	// "cool, probe once, fail, cool longer" forever, so it keeps consuming a
	// probe every minute and never leaves the candidate set for good. Once the
	// sliding duration has saturated and the pair still cannot serve a request,
	// the model is what is broken, so only that model is disabled; the channel
	// keeps serving its other models. Zero disables the escalation.
	CooldownDisableStreak int `json:"cooldown_disable_streak"`
}

// DefaultChannelHealthSetting returns the recommended defaults.
func DefaultChannelHealthSetting() *ChannelHealthSetting {
	return &ChannelHealthSetting{
		Enabled:                    true,
		Alpha:                      0.3,
		MinScore:                   0.05,
		MinRequests:                5,
		CooldownThreshold:          5,
		CooldownBaseSeconds:        10,
		CooldownMaxSeconds:         60,
		CooldownMaxEjectionPercent: 50,
		CooldownAlpha:              0.3,
		CooldownDisableStreak:      3,
	}
}

// channelHealthSetting holds the runtime config. It is read on every request from
// handler goroutines while an admin may replace it at any time, so it is stored in
// an atomic.Pointer: the struct is swapped wholesale rather than mutated in place.
var channelHealthSetting atomic.Pointer[ChannelHealthSetting]

func init() {
	channelHealthSetting.Store(DefaultChannelHealthSetting())
}

// GetChannelHealthSetting returns the current channel health setting.
func GetChannelHealthSetting() *ChannelHealthSetting {
	return channelHealthSetting.Load()
}

// healthStateResetHook is invoked when the kill switch transitions from enabled
// to disabled, so accumulated per-channel health state is discarded instead of
// being resurrected on re-enable. It is registered by the model package because
// operation_setting must not import model: model already imports
// operation_setting, and the reverse edge would create an import cycle.
//
// Stored atomically because SetChannelHealthSetting reads it from whichever
// goroutine performs the toggle, while registration happens during package init.
var healthStateResetHook atomic.Pointer[func()]

// RegisterHealthStateResetHook wires the reset callback. Passing nil clears it.
func RegisterHealthStateResetHook(hook func()) {
	if hook == nil {
		healthStateResetHook.Store(nil)
		return
	}
	healthStateResetHook.Store(&hook)
}

// SetChannelHealthSetting updates the channel health setting. The caller's
// struct is copied before publication so the caller cannot mutate the live
// config after this call returns.
func SetChannelHealthSetting(cfg *ChannelHealthSetting) {
	if cfg == nil {
		return
	}
	normalized := *cfg // copy-on-write; never mutate the caller's struct
	normalizeChannelHealthSetting(&normalized)

	// Swap installs the new config and hands back the previous one, so the
	// enabled -> disabled edge is derived from the exact pointer this call
	// replaced. Reading the old value separately would let two concurrent
	// toggles observe a predecessor they did not actually replace, firing or
	// skipping the reset hook incorrectly.
	previous := channelHealthSetting.Swap(&normalized)
	wasEnabled := previous != nil && previous.Enabled
	if wasEnabled && !normalized.Enabled {
		if hook := healthStateResetHook.Load(); hook != nil {
			(*hook)()
		}
	}
}

// normalizeChannelHealthSetting clamps all fields into their valid ranges in
// place. It uses math-free integer arithmetic only.
func normalizeChannelHealthSetting(cfg *ChannelHealthSetting) {
	if cfg.Alpha < 0 {
		cfg.Alpha = 0
	}
	if cfg.Alpha > 1 {
		cfg.Alpha = 1
	}
	if cfg.MinScore < 0 {
		cfg.MinScore = 0
	}
	if cfg.MinScore > 1 {
		cfg.MinScore = 1
	}
	if cfg.MinRequests < 0 {
		cfg.MinRequests = 0
	}
	if cfg.CooldownThreshold < 1 {
		cfg.CooldownThreshold = 1
	}
	if cfg.CooldownBaseSeconds < 0 {
		cfg.CooldownBaseSeconds = 0
	}
	if cfg.CooldownMaxSeconds < 0 {
		cfg.CooldownMaxSeconds = 0
	}
	if cfg.CooldownMaxSeconds < cfg.CooldownBaseSeconds {
		cfg.CooldownMaxSeconds = cfg.CooldownBaseSeconds
	}
	if cfg.CooldownMaxEjectionPercent < 0 {
		cfg.CooldownMaxEjectionPercent = 0
	}
	if cfg.CooldownMaxEjectionPercent > 100 {
		cfg.CooldownMaxEjectionPercent = 100
	}
	if cfg.CooldownAlpha < 0 {
		cfg.CooldownAlpha = 0
	}
	if cfg.CooldownAlpha > 1 {
		cfg.CooldownAlpha = 1
	}
	if cfg.CooldownDisableStreak < 0 {
		cfg.CooldownDisableStreak = 0
	}
}

// healthOptionKey is the flat option key for each field.
const (
	OptChannelHealthEnabled                = "ChannelHealthEnabled"
	OptChannelHealthCooldownThreshold      = "ChannelHealthCooldownThreshold"
	OptChannelHealthCooldownBaseSeconds    = "ChannelHealthCooldownBaseSeconds"
	OptChannelHealthCooldownMaxSeconds     = "ChannelHealthCooldownMaxSeconds"
	OptChannelHealthCooldownMaxEjectionPct = "ChannelHealthCooldownMaxEjectionPercent"
	OptChannelHealthCooldownAlpha          = "ChannelHealthCooldownAlpha"
	OptChannelHealthCooldownDisableStreak  = "ChannelHealthCooldownDisableStreak"
)

// healthOptionKeys lists the recognized health flat option keys.
var healthOptionKeys = map[string]bool{
	OptChannelHealthEnabled:                true,
	OptChannelHealthCooldownThreshold:      true,
	OptChannelHealthCooldownBaseSeconds:    true,
	OptChannelHealthCooldownMaxSeconds:     true,
	OptChannelHealthCooldownMaxEjectionPct: true,
	OptChannelHealthCooldownAlpha:          true,
	OptChannelHealthCooldownDisableStreak:  true,
}

// IsChannelHealthOptionKey reports whether key is one of the recognized
// health flat option keys.
func IsChannelHealthOptionKey(key string) bool {
	return healthOptionKeys[key]
}

// ValidateChannelHealthSettingValue checks one health option without publishing
// it, so callers reject a bad value before it is persisted. Range checking lives
// here rather than relying on normalization: a clamped value would be stored in
// OptionMap verbatim while the runtime config held the clamped number, so the
// admin UI would keep showing a setting that is not the one in effect.
func ValidateChannelHealthSettingValue(key, value string) error {
	if !healthOptionKeys[key] {
		return fmt.Errorf("unknown channel health option key: %s", key)
	}
	current := channelHealthSetting.Load()
	if current == nil {
		return errors.New("channel health setting not initialized")
	}
	updated := *current
	if err := applyHealthOptionValue(&updated, key, value); err != nil {
		return err
	}
	return validateHealthRange(&updated, key)
}

// validateHealthRange rejects a parsed value that normalization would silently
// clamp. Only the field named by key is checked, so a pre-existing out-of-range
// value elsewhere in the config cannot block an unrelated update. The base/max
// pair is the one cross-field rule: normalization raises an inverted max up to
// base, which would leave the stored option disagreeing with the effective one,
// so an inversion is reported instead.
func validateHealthRange(cfg *ChannelHealthSetting, key string) error {
	switch key {
	case OptChannelHealthCooldownThreshold:
		if cfg.CooldownThreshold < 1 {
			return fmt.Errorf("%s must be at least 1", key)
		}
	case OptChannelHealthCooldownBaseSeconds:
		if cfg.CooldownBaseSeconds < 0 {
			return fmt.Errorf("%s must not be negative", key)
		}
		if cfg.CooldownBaseSeconds > cfg.CooldownMaxSeconds {
			return fmt.Errorf("%s must not exceed %s (%d)",
				key, OptChannelHealthCooldownMaxSeconds, cfg.CooldownMaxSeconds)
		}
	case OptChannelHealthCooldownMaxSeconds:
		if cfg.CooldownMaxSeconds < 0 {
			return fmt.Errorf("%s must not be negative", key)
		}
		if cfg.CooldownMaxSeconds < cfg.CooldownBaseSeconds {
			return fmt.Errorf("%s must not be below %s (%d)",
				key, OptChannelHealthCooldownBaseSeconds, cfg.CooldownBaseSeconds)
		}
	case OptChannelHealthCooldownDisableStreak:
		if cfg.CooldownDisableStreak < 0 {
			return fmt.Errorf("%s must not be negative", key)
		}
	case OptChannelHealthCooldownMaxEjectionPct:
		if cfg.CooldownMaxEjectionPercent < 0 || cfg.CooldownMaxEjectionPercent > 100 {
			return fmt.Errorf("%s must be between 0 and 100", key)
		}
	case OptChannelHealthCooldownAlpha:
		if cfg.CooldownAlpha < 0 || cfg.CooldownAlpha > 1 {
			return fmt.Errorf("%s must be between 0 and 1", key)
		}
	}
	return nil
}

// UpdateChannelHealthSettingValue parses one recognized flat option key and
// CAS-updates only that field on a copy of the current atomic config. The value
// is range-checked before publication, so what an operator submits is exactly
// what takes effect; normalization then only has to guard legacy configs loaded
// from the database. Unknown keys, malformed values, and out-of-range values all
// return an error without changing the config.
func UpdateChannelHealthSettingValue(key, value string) error {
	if !healthOptionKeys[key] {
		return fmt.Errorf("unknown channel health option key: %s", key)
	}
	for {
		current := channelHealthSetting.Load()
		if current == nil {
			return errors.New("channel health setting not initialized")
		}
		updated := *current
		if err := applyHealthOptionValue(&updated, key, value); err != nil {
			return err
		}
		if err := validateHealthRange(&updated, key); err != nil {
			return err
		}
		normalizeChannelHealthSetting(&updated)
		if channelHealthSetting.CompareAndSwap(current, &updated) {
			// Fire the enabled->disabled reset hook only when this update
			// actually crossed the edge.
			if current.Enabled && !updated.Enabled {
				if hook := healthStateResetHook.Load(); hook != nil {
					(*hook)()
				}
			}
			return nil
		}
		// CAS failed: a concurrent update replaced the config; retry from
		// the newest pointer.
	}
}

// applyHealthOptionValue mutates cfg in place with the parsed value for key.
// It does not normalize; normalization happens once before publication.
func applyHealthOptionValue(cfg *ChannelHealthSetting, key, value string) error {
	switch key {
	case OptChannelHealthEnabled:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid boolean for %s: %w", key, err)
		}
		cfg.Enabled = b
	case OptChannelHealthCooldownThreshold:
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer for %s: %w", key, err)
		}
		cfg.CooldownThreshold = n
	case OptChannelHealthCooldownBaseSeconds:
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer for %s: %w", key, err)
		}
		cfg.CooldownBaseSeconds = n
	case OptChannelHealthCooldownMaxSeconds:
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer for %s: %w", key, err)
		}
		cfg.CooldownMaxSeconds = n
	case OptChannelHealthCooldownDisableStreak:
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer for %s: %w", key, err)
		}
		cfg.CooldownDisableStreak = n
	case OptChannelHealthCooldownMaxEjectionPct:
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer for %s: %w", key, err)
		}
		cfg.CooldownMaxEjectionPercent = n
	case OptChannelHealthCooldownAlpha:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid float for %s: %w", key, err)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("invalid finite float for %s", key)
		}
		cfg.CooldownAlpha = f
	}
	return nil
}
