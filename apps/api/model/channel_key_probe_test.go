package model

import (
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withKeyProbe installs a probe verdict and restores the previous wiring, so one
// case cannot leak its verdict into the next.
func withKeyProbe(t *testing.T, valid, decisive bool) *int {
	t.Helper()
	previous := ProbeChannelKeyFunc
	calls := 0
	ProbeChannelKeyFunc = func(int, int) (bool, bool) {
		calls++
		return valid, decisive
	}
	t.Cleanup(func() { ProbeChannelKeyFunc = previous })
	return &calls
}

// seedMultiKeyChannel creates a two-key channel serving two models, which is the
// shape the cascade has to distinguish: one key dying must not take the other
// key's units with it.
func seedMultiKeyChannel(t *testing.T, id int) *Channel {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&Channel{}, &Ability{}, &GatewayConfigRevision{}, &GatewayConfigOutbox{}))
	require.NoError(t, InitializeGatewayConfigRevision())
	channel := Channel{
		Id:     id,
		Type:   constant.ChannelTypeOpenAI,
		Key:    "key-a\nkey-b",
		Status: common.ChannelStatusEnabled,
		Name:   "cascade-channel-" + strconv.Itoa(id),
		Models: "model-x,model-y",
		Group:  "default",
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
			MultiKeyMode: constant.MultiKeyModeRandom,
		},
	}
	require.NoError(t, DB.Create(&channel).Error)
	return &channel
}

// TestVerifyKeyAndCascadeDisablesEveryModelOfTheKey covers the Wave F contract:
// a conclusive 401/403 verdict means the key itself is dead, so every model unit
// behind that key index is disabled and the channel's key status is updated.
// The sibling key index must stay healthy — that is the whole reason the
// scheduling unit carries a key index.
func TestVerifyKeyAndCascadeDisablesEveryModelOfTheKey(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())
	calls := withKeyProbe(t, false, true)
	channel := seedMultiKeyChannel(t, 9210)
	now := time.Now()

	verifyKeyAndCascade(channel.Id, 0, now)

	assert.Equal(t, 1, *calls, "the cascade must probe exactly once")
	for _, name := range []string{"model-x", "model-y"} {
		state, _ := GetRouteHealth(RouteKey{ChannelId: channel.Id, KeyIndex: 0, Model: name})
		assert.Equal(t, HealthDisabled, state, "model %s behind the dead key must be disabled", name)
		sibling := RouteKey{ChannelId: channel.Id, KeyIndex: 1, Model: name}
		assert.True(t, IsRouteHealthy(sibling, now), "model %s on the surviving key must stay healthy", name)
	}

	var stored Channel
	require.NoError(t, DB.First(&stored, "id = ?", channel.Id).Error)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.ChannelInfo.MultiKeyStatusList[0],
		"the channel's key status must record the dead key")
}

// TestVerifyKeyAndCascadeIgnoresInconclusiveProbe covers the 429/5xx/timeout
// path: without a conclusive verdict the cascade must change nothing, otherwise
// one upstream hiccup would disable a whole key's worth of models.
func TestVerifyKeyAndCascadeIgnoresInconclusiveProbe(t *testing.T) {
	withRouteHealthDB(t)
	withHealthSetting(t, operation_setting.DefaultChannelModelHealthSetting())
	withKeyProbe(t, false, false)
	channel := seedMultiKeyChannel(t, 9211)
	now := time.Now()

	verifyKeyAndCascade(channel.Id, 0, now)

	for _, name := range []string{"model-x", "model-y"} {
		assert.True(t, IsRouteHealthy(RouteKey{ChannelId: channel.Id, KeyIndex: 0, Model: name}, now),
			"an inconclusive probe must not disable model %s", name)
	}
	var stored Channel
	require.NoError(t, DB.First(&stored, "id = ?", channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status, "channel status must be untouched")
}

// TestVerifyKeyAndCascadeSkipsWhenProbeDisabled proves KeyProbeEnabled is a real
// kill switch: with probing off the upstream is never contacted at all.
func TestVerifyKeyAndCascadeSkipsWhenProbeDisabled(t *testing.T) {
	withRouteHealthDB(t)
	cfg := operation_setting.DefaultChannelModelHealthSetting()
	cfg.KeyProbeEnabled = false
	withHealthSetting(t, cfg)
	calls := withKeyProbe(t, false, true)
	channel := seedMultiKeyChannel(t, 9212)

	verifyKeyAndCascade(channel.Id, 0, time.Now())

	assert.Zero(t, *calls, "a disabled probe must never reach upstream")
}
