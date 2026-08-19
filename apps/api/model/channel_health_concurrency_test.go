package model

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// TestChannelHealthSettingSwapIsRaceFree guards the runtime config against the
// data race opencodereview found: GetChannelHealthSetting is called on every
// request from handler goroutines, while an admin toggling the kill switch
// replaces the pointer. Before the fix this was an unsynchronized read/write that
// -race flagged, and SetChannelHealthSetting's read-modify-write additionally let
// two concurrent toggles derive wasEnabled from a predecessor they had not
// actually replaced, firing or skipping the reset hook incorrectly.
//
// Run under -race; without atomic.Pointer this fails.
func TestChannelHealthSettingSwapIsRaceFree(t *testing.T) {
	resetHealthManager()
	setTestConfig(true, 0.3, 0.05, 5)
	t.Cleanup(func() { setTestConfig(true, 0.3, 0.05, 5) })

	mgr := GetChannelHealthManager()

	var wg sync.WaitGroup

	// Writers: toggle the kill switch, exercising both edges.
	for writer := range 2 {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for i := range 100 {
				operation_setting.SetChannelHealthSetting(&operation_setting.ChannelHealthSetting{
					Enabled:     (i+writer)%2 == 0,
					Alpha:       0.3,
					MinScore:    0.05,
					MinRequests: 5,
				})
			}
		}(writer)
	}

	// Readers: the request path, which reads the config on every call.
	for reader := range 4 {
		wg.Add(1)
		go func(reader int) {
			defer wg.Done()
			channelID := 9800 + reader
			for range 200 {
				_ = mgr.EffectiveWeight(channelID, 10)
				mgr.RecordChannelOutcome(channelID, OutcomeSuccess)
				_ = mgr.GetScore(channelID)
			}
		}(reader)
	}

	wg.Wait()

	// The config must still be readable and internally consistent afterwards.
	cfg := operation_setting.GetChannelHealthSetting()
	assert.NotNil(t, cfg, "the config pointer must never be left nil")
	assert.InDelta(t, 0.3, cfg.Alpha, 1e-9)
	assert.Equal(t, 5, cfg.MinRequests)
}

// TestRegisterHealthStateResetHookAcceptsNil covers the clearing path, which the
// atomic.Pointer rewrite made explicit: storing a nil func value would panic on
// dereference, so nil must clear the slot instead.
func TestRegisterHealthStateResetHookAcceptsNil(t *testing.T) {
	t.Cleanup(func() {
		operation_setting.RegisterHealthStateResetHook(func() {
			GetChannelHealthManager().Reset()
		})
	})

	operation_setting.RegisterHealthStateResetHook(nil)

	// With no hook registered, toggling the kill switch off must be a no-op rather
	// than a nil dereference.
	setTestConfig(true, 0.3, 0.05, 5)
	assert.NotPanics(t, func() { setTestConfig(false, 0.3, 0.05, 5) },
		"a cleared hook must not be invoked")
}
