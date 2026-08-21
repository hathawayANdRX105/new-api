package model

import (
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
)

const (
	HealthHealthy  = "healthy"
	HealthCalm     = "calm"
	HealthDormant  = "dormant"
	HealthDisabled = "disabled"
)

type RouteKey struct {
	ChannelId int
	Model     string
}

type ChannelModelHealth struct {
	ChannelId           int    `gorm:"primaryKey"`
	Model               string `gorm:"primaryKey;size:255"`
	State               string `gorm:"size:16;not null;default:healthy"`
	IsolationLevel      int    `gorm:"not null;default:0"`
	Until               *int64 `gorm:"bigint"`
	Version             int    `gorm:"not null;default:1"`
	DormantDisableCount int    `gorm:"not null;default:0"`
	LastErrorCode       string `gorm:"size:64"`
	LastErrorAt         *int64 `gorm:"bigint"`
	LastSuccessAt       *int64 `gorm:"bigint"`
	UpdatedAt           int64  `gorm:"bigint"`
}

func (ChannelModelHealth) TableName() string { return "channel_model_health" }

type routeHealthState struct {
	State               string
	IsolationLevel      int
	Until               *int64
	Version             int
	DormantDisableCount int
}

var routeHealthIDM = map[RouteKey]*routeHealthState{}
var routeHealthLock sync.RWMutex

func IsRouteHealthy(key RouteKey, now time.Time) bool {
	routeHealthLock.RLock()
	state := routeHealthIDM[key]
	routeHealthLock.RUnlock()
	if state == nil || state.State == HealthHealthy {
		return true
	}
	if state.State == HealthDisabled {
		return false
	}
	if state.Until == nil || *state.Until > now.Unix() {
		return false
	}

	result := DB.Model(&ChannelModelHealth{}).
		Where("channel_id = ? AND model = ? AND version = ?", key.ChannelId, key.Model, state.Version).
		Updates(map[string]interface{}{"state": HealthHealthy, "until": nil, "version": state.Version + 1, "updated_at": now.Unix()})
	if result.Error != nil {
		common.SysError("failed to expire channel model health: " + result.Error.Error())
		return false
	}
	if result.RowsAffected != 0 {
		cacheHealth(&ChannelModelHealth{ChannelId: key.ChannelId, Model: key.Model, State: HealthHealthy, IsolationLevel: state.IsolationLevel, Version: state.Version + 1, DormantDisableCount: state.DormantDisableCount})
		return true
	}

	var row ChannelModelHealth
	if err := DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&row).Error; err != nil {
		common.SysError("failed to refresh channel model health after expiry CAS: " + err.Error())
		return false
	}
	cacheHealth(&row)
	return row.State == HealthHealthy || (row.State != HealthDisabled && (row.Until == nil || *row.Until <= now.Unix()))
}
func GetRouteHealth(key RouteKey) (string, bool) {
	routeHealthLock.RLock()
	state := routeHealthIDM[key]
	routeHealthLock.RUnlock()
	if state == nil {
		return HealthHealthy, true
	}
	return state.State, state.State == HealthHealthy
}

// GetRouteIsolation exposes the cached isolation snapshot so the relay layer can
// log a state transition with the request id attached. ok is false when the
// route has no isolation record, which is the healthy default.
func GetRouteIsolation(key RouteKey) (state string, level int, until int64, ok bool) {
	routeHealthLock.RLock()
	cached := routeHealthIDM[key]
	routeHealthLock.RUnlock()
	if cached == nil {
		return HealthHealthy, 0, 0, false
	}
	if cached.Until != nil {
		until = *cached.Until
	}
	return cached.State, cached.IsolationLevel, until, true
}
func cacheHealth(row *ChannelModelHealth) {
	var until *int64
	if row.Until != nil {
		v := *row.Until
		until = &v
	}
	routeHealthLock.Lock()
	routeHealthIDM[RouteKey{row.ChannelId, row.Model}] = &routeHealthState{row.State, row.IsolationLevel, until, row.Version, row.DormantDisableCount}
	routeHealthLock.Unlock()
}
func ClearRouteHealthCache() {
	routeHealthLock.Lock()
	routeHealthIDM = map[RouteKey]*routeHealthState{}
	routeHealthLock.Unlock()
}

// InitChannelModelHealthCache loads persisted route state once at startup. The
// selectors still perform only an in-process lookup; rows created after startup
// are inserted directly by RecordRetryableFailure and mirrored immediately.
func InitChannelModelHealthCache() {
	var rows []ChannelModelHealth
	if err := DB.Find(&rows).Error; err != nil {
		common.SysError("failed to load channel model health cache: " + err.Error())
		return
	}
	cache := make(map[RouteKey]*routeHealthState, len(rows))
	for _, row := range rows {
		var until *int64
		if row.Until != nil {
			value := *row.Until
			until = &value
		}
		cache[RouteKey{ChannelId: row.ChannelId, Model: row.Model}] = &routeHealthState{
			State:               row.State,
			IsolationLevel:      row.IsolationLevel,
			Until:               until,
			Version:             row.Version,
			DormantDisableCount: row.DormantDisableCount,
		}
	}
	routeHealthLock.Lock()
	routeHealthIDM = cache
	routeHealthLock.Unlock()
	common.SysLog("channel model health cache loaded from database")
}

func isolationDuration(level int, cfg *operation_setting.ChannelModelHealthSetting) (string, int64) {
	switch {
	case level <= 0:
		return HealthHealthy, 0
	case level <= 3:
		return HealthCalm, int64(cfg.CalmFastBase + (level-1)*cfg.CalmFastInterval)
	case level <= 6:
		return HealthCalm, int64(cfg.CalmSlowBase + (level-4)*cfg.CalmSlowInterval)
	case level <= 9:
		return HealthDormant, int64(cfg.DormantBase + (level-7)*cfg.DormantInterval)
	default:
		return HealthDormant, int64(cfg.DormantMaxBase)
	}
}

// casMaxAttempts bounds the optimistic retry loop. A fixed small bound silently
// drops failures once several requests race on the same route: with N writers a
// loser can lose N-1 times in a row, so the ladder would under-count and the
// route would stay selectable longer than configured. The bound is generous
// because each lost attempt only costs one indexed read plus one failed update.
const casMaxAttempts = 16

// casBackoff spreads retries so contending writers do not re-collide in lockstep.
func casBackoff(attempt int) {
	if attempt <= 0 {
		return
	}
	time.Sleep(time.Duration(attempt) * 200 * time.Microsecond)
}

// RecordRetryableFailure persists one retry-eligible failure using optimistic CAS.
func RecordRetryableFailure(key RouteKey, errorCode string, now time.Time) error {
	cfg := operation_setting.GetChannelModelHealthSetting()
	for attempt := 0; attempt < casMaxAttempts; attempt++ {
		casBackoff(attempt)
		var row ChannelModelHealth
		if err := DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&row).Error; err != nil {
			if err != gorm.ErrRecordNotFound {
				return err
			}
			row = ChannelModelHealth{ChannelId: key.ChannelId, Model: key.Model, State: HealthHealthy, Version: 1}
			if err := DB.Create(&row).Error; err != nil {
				continue
			}
		}
		level := row.IsolationLevel + 1
		state, seconds := isolationDuration(level, cfg)
		if row.State == HealthDormant && row.Until != nil && *row.Until <= now.Unix() {
			row.DormantDisableCount++
			if cfg.DormantDisableThreshold > 0 && row.DormantDisableCount >= cfg.DormantDisableThreshold {
				state, seconds = HealthDisabled, 0
			}
		}
		until := (*int64)(nil)
		if state != HealthDisabled {
			deadline := now.Unix() + seconds
			until = &deadline
		}
		q := DB.Model(&ChannelModelHealth{}).Where("channel_id = ? AND model = ? AND version = ?", key.ChannelId, key.Model, row.Version).Updates(map[string]interface{}{"state": state, "isolation_level": level, "until": until, "version": row.Version + 1, "dormant_disable_count": row.DormantDisableCount, "last_error_code": errorCode, "last_error_at": now.Unix(), "updated_at": now.Unix()})
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected == 0 {
			continue
		}
		row.State, row.IsolationLevel, row.Until, row.Version = state, level, until, row.Version+1
		row.LastErrorCode, row.UpdatedAt = errorCode, now.Unix()
		cacheHealth(&row)
		return nil
	}
	return errors.New("channel model health state changed concurrently")
}

func RecoverRoute(key RouteKey, now time.Time) error {
	return updateRouteState(key, HealthHealthy, 0, nil, 0, now)
}
func DisableRoute(key RouteKey, now time.Time) error {
	return updateRouteState(key, HealthDisabled, 0, nil, 0, now)
}
func updateRouteState(key RouteKey, state string, level int, until *int64, dormantCount int, now time.Time) error {
	for attempt := 0; attempt < casMaxAttempts; attempt++ {
		casBackoff(attempt)
		var row ChannelModelHealth
		if err := DB.Where("channel_id = ? AND model = ?", key.ChannelId, key.Model).First(&row).Error; err != nil {
			if err != gorm.ErrRecordNotFound {
				return err
			}
			row = ChannelModelHealth{ChannelId: key.ChannelId, Model: key.Model, State: HealthHealthy, Version: 1}
			if err := DB.Create(&row).Error; err != nil {
				continue
			}
		}
		q := DB.Model(&ChannelModelHealth{}).Where("channel_id = ? AND model = ? AND version = ?", key.ChannelId, key.Model, row.Version).Updates(map[string]interface{}{"state": state, "isolation_level": level, "until": until, "version": row.Version + 1, "dormant_disable_count": dormantCount, "updated_at": now.Unix()})
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected == 0 {
			continue
		}
		row.State, row.IsolationLevel, row.Until, row.Version, row.DormantDisableCount, row.UpdatedAt = state, level, until, row.Version+1, dormantCount, now.Unix()
		cacheHealth(&row)
		return nil
	}
	return errors.New("channel model health state changed concurrently")
}

// ListChannelModelHealth returns the persisted isolation rows. A positive
// channelID narrows the result to one channel, which is what the channel detail
// panel needs; 0 returns every row for a system-wide view.
func ListChannelModelHealth(channelID int) ([]ChannelModelHealth, error) {
	var rows []ChannelModelHealth
	query := DB.Order("channel_id, model")
	if channelID > 0 {
		query = query.Where("channel_id = ?", channelID)
	}
	err := query.Find(&rows).Error
	return rows, err
}
