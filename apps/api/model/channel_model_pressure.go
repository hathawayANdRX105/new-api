package model

import (
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// PressureLevel classifies pool availability for a model's schedulable units.
type PressureLevel int

const (
	PressureNormal PressureLevel = iota
	PressureWarning
	PressureEmergency
)

// modelPressure tracks per-model availability counts for the hot-path
// pressure check. total = all schedulable units; healthy = those currently
// in the healthy state. A cache miss counts as healthy, so healthy starts
// equal to total and is adjusted incrementally as state transitions occur.
type modelPressure struct {
	total   int
	healthy int
}

var pressureIDM = map[string]*modelPressure{}
var pressureLock sync.RWMutex

// modelPressureLevel reads the in-process counter with zero DB reads and zero
// traversal. total == 0 or no record → PressureNormal (fail-safe direction).
func modelPressureLevel(model string) PressureLevel {
	pressureLock.RLock()
	p := pressureIDM[model]
	pressureLock.RUnlock()
	if p == nil || p.total == 0 {
		return PressureNormal
	}
	ratio := float64(p.healthy) * 100 / float64(p.total)
	cfg := operation_setting.GetChannelModelHealthSetting()
	switch {
	case ratio < float64(cfg.EmergencyThreshold):
		return PressureEmergency
	case ratio < float64(cfg.WarningThreshold):
		return PressureWarning
	default:
		return PressureNormal
	}
}

// decayStep returns the isolation_level decrement based on pool pressure:
// warning → AcceleratedDecayStep; normal/emergency → NormalDecayStep.
// Config values <= 0 fall back to 1 so decay never stalls at step 0.
func decayStep(model string) int {
	cfg := operation_setting.GetChannelModelHealthSetting()
	step := cfg.NormalDecayStep
	if modelPressureLevel(model) == PressureWarning {
		step = cfg.AcceleratedDecayStep
	}
	if step <= 0 {
		step = 1
	}
	return step
}

// isHealthyState reports whether a state string represents the healthy state.
func isHealthyState(state string) bool {
	return state == HealthHealthy
}

// pressureOnStateChange adjusts the healthy counter when a route crosses the
// healthy ↔ non-healthy boundary. Same-state migrations (e.g. calm→dormant)
// do not move the counter. healthy is floored at zero.
func pressureOnStateChange(key RouteKey, from string, to string) {
	fromHealthy := isHealthyState(from)
	toHealthy := isHealthyState(to)
	if fromHealthy == toHealthy {
		return
	}
	pressureLock.Lock()
	defer pressureLock.Unlock()
	p := pressureIDM[key.Model]
	if p == nil {
		return
	}
	if fromHealthy && !toHealthy {
		p.healthy--
	} else {
		p.healthy++
	}
	if p.healthy < 0 {
		p.healthy = 0
	}
}

// pressureOnRemove decrements total (and healthy if the unit was healthy)
// when a schedulable unit is cleaned up. healthy is floored at zero.
func pressureOnRemove(key RouteKey) {
	pressureLock.Lock()
	defer pressureLock.Unlock()
	p := pressureIDM[key.Model]
	if p == nil {
		return
	}
	p.total--
	if p.total < 0 {
		p.total = 0
	}
	// A cache miss counts as healthy, so a removed unit is assumed healthy
	// unless the in-process state says otherwise.
	st, _ := GetRouteHealth(key)
	if st == "" || st == HealthHealthy {
		p.healthy--
		if p.healthy < 0 {
			p.healthy = 0
		}
	}
}

// pressureRecomputeTotals rebuilds the pressure map from scratch: total =
// distinct channels × keys per channel for each model (from enabled abilities
// and channel info); healthy = total minus non-healthy persisted rows.
func pressureRecomputeTotals() {
	type abilityRow struct {
		Model     string
		ChannelId int
	}
	var abilities []abilityRow
	if err := DB.Model(&Ability{}).Select("model, channel_id").Where("enabled = ?", true).Find(&abilities).Error; err != nil {
		common.SysError("pressure recompute: query abilities failed: " + err.Error())
		return
	}

	var channels []Channel
	if err := DB.Select("id, channel_info").Find(&channels).Error; err != nil {
		common.SysError("pressure recompute: query channels failed: " + err.Error())
		return
	}
	multiKeySize := make(map[int]int, len(channels))
	for _, ch := range channels {
		size := ch.ChannelInfo.MultiKeySize
		if size <= 0 {
			size = 1
		}
		multiKeySize[ch.Id] = size
	}

	modelChannels := make(map[string]map[int]struct{})
	for _, a := range abilities {
		set, ok := modelChannels[a.Model]
		if !ok {
			set = make(map[int]struct{})
			modelChannels[a.Model] = set
		}
		set[a.ChannelId] = struct{}{}
	}

	totals := make(map[string]int, len(modelChannels))
	for model, chSet := range modelChannels {
		t := 0
		for chID := range chSet {
			t += multiKeySize[chID]
		}
		totals[model] = t
	}

	var healthRows []ChannelModelHealth
	if err := DB.Find(&healthRows).Error; err != nil {
		common.SysError("pressure recompute: query health rows failed: " + err.Error())
		return
	}

	nonHealthy := make(map[string]int)
	for _, row := range healthRows {
		if row.State == HealthHealthy {
			continue
		}
		if _, tracked := totals[row.Model]; !tracked {
			continue
		}
		nonHealthy[row.Model]++
	}

	newMap := make(map[string]*modelPressure, len(totals))
	for model, total := range totals {
		healthy := total - nonHealthy[model]
		if healthy < 0 {
			healthy = 0
		}
		newMap[model] = &modelPressure{total: total, healthy: healthy}
	}

	pressureLock.Lock()
	pressureIDM = newMap
	pressureLock.Unlock()
	common.SysLog("channel model pressure recompute complete")
}

// maybeEmergencyRecover synchronously batch-recovers routes when a model's
// availability drops below EmergencyThreshold. It picks the least-isolated
// non-disabled routes (by isolation_level ASC, updated_at ASC) and resets
// their state to healthy while preserving isolation_level — so a subsequent
// failure resumes from the original level, not from zero.
func maybeEmergencyRecover(model string, now time.Time) {
	pressureLock.RLock()
	p := pressureIDM[model]
	pressureLock.RUnlock()
	if p == nil || p.total == 0 {
		return
	}

	cfg := operation_setting.GetChannelModelHealthSetting()
	ratio := float64(p.healthy) * 100 / float64(p.total)
	if ratio >= float64(cfg.EmergencyThreshold) {
		return
	}

	// Gap = units needed to reach WarningThreshold availability.
	want := int(math.Ceil(float64(p.total)*float64(cfg.WarningThreshold)/100)) - p.healthy
	if want <= 0 {
		return
	}

	var rows []ChannelModelHealth
	if err := DB.Where("model = ? AND state <> ?", model, HealthDisabled).
		Order("isolation_level ASC, updated_at ASC").
		Limit(want).
		Find(&rows).Error; err != nil {
		common.SysError("emergency recover query failed: " + err.Error())
		return
	}

	for _, row := range rows {
		key := RouteKey{ChannelId: row.ChannelId, KeyIndex: row.KeyIndex, Model: row.Model}
		if err := updateRouteState(key, HealthHealthy, row.IsolationLevel, nil, row.DormantDisableCount, now); err != nil {
			common.SysError("emergency recover failed: channel=" + strconv.Itoa(row.ChannelId) + " key=" + strconv.Itoa(row.KeyIndex) + " model=" + row.Model + " err=" + err.Error())
			continue
		}
		logger.LogWarn(nil, "emergency recover route: channel="+strconv.Itoa(row.ChannelId)+" key="+strconv.Itoa(row.KeyIndex)+" model="+row.Model+" level="+strconv.Itoa(row.IsolationLevel))
	}
}
