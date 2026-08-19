package operation_setting

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

// channelHealthSetting holds the runtime config. It is read on every request,
// so updates take effect immediately without restart.
var channelHealthSetting = DefaultChannelHealthSetting()

// GetChannelHealthSetting returns the current channel health setting.
func GetChannelHealthSetting() *ChannelHealthSetting {
	return channelHealthSetting
}

// healthStateResetHook is invoked when the kill switch transitions from enabled
// to disabled, so accumulated per-channel health state is discarded instead of
// being resurrected on re-enable. It is registered by the model package because
// operation_setting must not import model: model already imports
// operation_setting, and the reverse edge would create an import cycle.
var healthStateResetHook func()

// RegisterHealthStateResetHook wires the reset callback. Passing nil clears it.
func RegisterHealthStateResetHook(hook func()) {
	healthStateResetHook = hook
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
	// Capture the previous kill-switch state before swapping the pointer so the
	// enabled -> disabled edge can be detected exactly once.
	wasEnabled := channelHealthSetting != nil && channelHealthSetting.Enabled
	channelHealthSetting = cfg
	if wasEnabled && !cfg.Enabled && healthStateResetHook != nil {
		healthStateResetHook()
	}
}
