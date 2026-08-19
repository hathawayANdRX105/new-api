package operation_setting

import "sync/atomic"

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
}

// DefaultChannelHealthSetting returns the recommended defaults.
func DefaultChannelHealthSetting() *ChannelHealthSetting {
	return &ChannelHealthSetting{
		Enabled:     true,
		Alpha:       0.3,
		MinScore:    0.05,
		MinRequests: 5,
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

// SetChannelHealthSetting updates the channel health setting.
func SetChannelHealthSetting(cfg *ChannelHealthSetting) {
	if cfg == nil {
		return
	}
	// Validate: clamp alpha to [0, 1], min_score to [0, 1], min_requests >= 0
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
	// Swap installs the new config and hands back the previous one, so the
	// enabled -> disabled edge is derived from the exact pointer this call
	// replaced. Reading the old value separately would let two concurrent
	// toggles observe a predecessor they did not actually replace, firing or
	// skipping the reset hook incorrectly.
	previous := channelHealthSetting.Swap(cfg)
	wasEnabled := previous != nil && previous.Enabled
	if wasEnabled && !cfg.Enabled {
		if hook := healthStateResetHook.Load(); hook != nil {
			(*hook)()
		}
	}
}
