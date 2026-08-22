package model

import (
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// ProbeChannelKeyFunc verifies one channel key without spending tokens and
// reports whether the answer is conclusive. It is wired by the controller layer
// (which owns the upstream model-list request construction) because this package
// must not import it. valid is meaningful only when decisive is true: a 429, a
// 5xx, or a timeout leaves the key's fate to the regular state machine.
var ProbeChannelKeyFunc func(channelID, keyIndex int) (valid bool, decisive bool)

// verifyKeyAndCascade runs after a scheduling unit trips the auto-disable
// threshold. One unit failing repeatedly can mean a dead model or a dead key, and
// only an upstream check tells them apart. When the key is confirmed invalid,
// every model unit behind it is disabled together with the key itself, so the
// remaining keys of a multi-key channel keep serving. An inconclusive probe
// changes nothing: the unit stays disabled, its siblings stay untouched.
func verifyKeyAndCascade(channelID, keyIndex int, now time.Time) {
	cfg := operation_setting.GetChannelModelHealthSetting()
	if !cfg.KeyProbeEnabled || ProbeChannelKeyFunc == nil {
		return
	}
	valid, decisive := ProbeChannelKeyFunc(channelID, keyIndex)
	if !decisive || valid {
		return
	}

	channel, err := GetChannelById(channelID, true)
	if err != nil || channel == nil {
		common.SysError("key verification cascade: channel " + strconv.Itoa(channelID) + " not found")
		return
	}
	for _, name := range channel.GetModels() {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if err := DisableRoute(RouteKey{ChannelId: channelID, KeyIndex: keyIndex, Model: name}, now); err != nil {
			common.SysError("key verification cascade: disable route failed: " + err.Error())
		}
	}

	// UpdateChannelStatus resolves the key string to its index and writes
	// MultiKeyStatusList for a multi-key channel; an empty key disables the
	// whole channel, which is the single-key shape and matches auto_ban.
	usingKey := ""
	if channel.ChannelInfo.IsMultiKey {
		keys := channel.GetKeys()
		if keyIndex < 0 || keyIndex >= len(keys) {
			return
		}
		usingKey = keys[keyIndex]
	}
	UpdateChannelStatus(channelID, usingKey, common.ChannelStatusAutoDisabled, "key verification failed")
	logger.LogWarn(nil, "key verification failed: channel="+strconv.Itoa(channelID)+" key="+strconv.Itoa(keyIndex)+" models="+strconv.Itoa(len(channel.GetModels())))
}
