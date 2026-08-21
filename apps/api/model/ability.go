package model

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/samber/lo"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Ability struct {
	Group     string  `json:"group" gorm:"type:varchar(64);primaryKey;autoIncrement:false"`
	Model     string  `json:"model" gorm:"type:varchar(255);primaryKey;autoIncrement:false"`
	ChannelId int     `json:"channel_id" gorm:"primaryKey;autoIncrement:false;index"`
	Enabled   bool    `json:"enabled"`
	Priority  *int64  `json:"priority" gorm:"bigint;default:0;index"`
	Weight    uint    `json:"weight" gorm:"default:0;index"`
	Tag       *string `json:"tag" gorm:"index"`
}

type AbilityWithChannel struct {
	Ability
	ChannelType int `json:"channel_type"`
}

func GetAllEnableAbilityWithChannels() ([]AbilityWithChannel, error) {
	var abilities []AbilityWithChannel
	err := DB.Table("abilities").
		Select("abilities.*, channels.type as channel_type").
		Joins("left join channels on abilities.channel_id = channels.id").
		Where("abilities.enabled = ?", true).
		Scan(&abilities).Error
	return abilities, err
}

func GetGroupEnabledModels(group string) []string {
	var models []string
	// Find distinct models
	DB.Table("abilities").Where(commonGroupCol+" = ? and enabled = ?", group, true).Distinct("model").Pluck("model", &models)
	return models
}

func GetEnabledModels() []string {
	var models []string
	// Find distinct models
	DB.Table("abilities").Where("enabled = ?", true).Distinct("model").Pluck("model", &models)
	return models
}

func getPriority(group string, model string, retry int) (int, error) {

	var priorities []int
	err := DB.Model(&Ability{}).
		Select("DISTINCT(priority)").
		Where(commonGroupCol+" = ? and model = ? and enabled = ?", group, model, true).
		Order("priority DESC").              // 按优先级降序排序
		Pluck("priority", &priorities).Error // Pluck用于将查询的结果直接扫描到一个切片中

	if err != nil {
		// 处理错误
		return 0, err
	}

	if len(priorities) == 0 {
		// 如果没有查询到优先级，则返回错误
		return 0, errors.New("数据库一致性被破坏")
	}

	// 确定要使用的优先级
	var priorityToUse int
	if retry >= len(priorities) {
		// 如果重试次数大于优先级数，则使用最小的优先级
		priorityToUse = priorities[len(priorities)-1]
	} else {
		priorityToUse = priorities[retry]
	}
	return priorityToUse, nil
}

func getChannelQuery(group string, model string, retry int) (*gorm.DB, error) {
	maxPrioritySubQuery := DB.Model(&Ability{}).Select("MAX(priority)").Where(commonGroupCol+" = ? and model = ? and enabled = ?", group, model, true)
	channelQuery := DB.Where(commonGroupCol+" = ? and model = ? and enabled = ? and priority = (?)", group, model, true, maxPrioritySubQuery)
	if retry != 0 {
		priority, err := getPriority(group, model, retry)
		if err != nil {
			return nil, err
		} else {
			channelQuery = DB.Where(commonGroupCol+" = ? and model = ? and enabled = ? and priority = ?", group, model, true, priority)
		}
	}

	return channelQuery, nil
}

func GetChannel(group string, model string, retry int, requestPath string, excludeSet map[int]bool) (*Channel, error) {
	var abilities []Ability

	var err error = nil
	channelQuery, err := getChannelQuery(group, model, retry)
	if err != nil {
		return nil, err
	}
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) || common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		err = channelQuery.Order("weight DESC").Find(&abilities).Error
	} else {
		err = channelQuery.Order("weight DESC").Find(&abilities).Error
	}
	if err != nil {
		return nil, err
	}
	abilities = filterAbilitiesByRequestPathAndModel(abilities, requestPath, model)
	// P1: filter out request-level excluded channels
	if excludeSet != nil {
		filtered := make([]Ability, 0, len(abilities))
		for _, a := range abilities {
			if !excludeSet[a.ChannelId] {
				filtered = append(filtered, a)
			}
		}
		abilities = filtered
	}
	if len(abilities) == 0 {
		return nil, nil
	}
	// Weighted random with EWMA health adjustment and capped cooldown ejection.
	healthMgr := GetChannelHealthManager()
	maxEjectionPercent := 0
	if cfg := operation_setting.GetChannelHealthSetting(); cfg != nil {
		maxEjectionPercent = cfg.CooldownMaxEjectionPercent
	}
	channelIDs := make([]int, 0, len(abilities))
	for _, ability := range abilities {
		channelIDs = append(channelIDs, ability.ChannelId)
	}
	ejected := healthMgr.FilterCoolingChannels(channelIDs, maxEjectionPercent)
	if len(ejected) > 0 {
		filtered := abilities[:0]
		for _, ability := range abilities {
			if !ejected[ability.ChannelId] {
				filtered = append(filtered, ability)
			}
		}
		abilities = filtered
	}
	if len(abilities) == 0 {
		return nil, nil
	}

	var weights []float64
	var totalWeight float64
	for _, ability := range abilities {
		effW := healthMgr.routingWeight(ability.ChannelId, routingBaseWeight(int(ability.Weight)), true)
		weights = append(weights, effW)
		totalWeight += effW
	}
	if totalWeight <= 0 {
		return nil, nil
	}
	channel := Channel{}
	randomWeight := rand.Float64() * totalWeight
	for i, ability_ := range abilities {
		randomWeight -= weights[i]
		if randomWeight < 0 || i == len(abilities)-1 {
			channel.Id = ability_.ChannelId
			break
		}
	}
	err = DB.First(&channel, "id = ?", channel.Id).Error
	return &channel, err
}

// filterAbilitiesByRequestPathAndModel restricts candidates by request path and
// model for the DB (non-memory-cache) selection path. Only Advanced Custom
// (type 58) channels are path-checked: kept only when one of their routes matches
// requestPath and model; all other channel types always pass. When requestPath is
// empty, filtering is skipped.
func filterAbilitiesByRequestPathAndModel(abilities []Ability, requestPath string, model string) []Ability {
	if requestPath == "" || len(abilities) == 0 {
		return abilities
	}

	channelIds := make([]int, 0, len(abilities))
	seen := make(map[int]struct{}, len(abilities))
	for _, ability := range abilities {
		if _, ok := seen[ability.ChannelId]; ok {
			continue
		}
		seen[ability.ChannelId] = struct{}{}
		channelIds = append(channelIds, ability.ChannelId)
	}

	var channels []*Channel
	if err := DB.Where("id IN ?", channelIds).Find(&channels).Error; err != nil {
		// On error, fall back to unfiltered candidates to avoid blocking selection
		return abilities
	}

	advancedConfigs := make(map[int]*dto.AdvancedCustomConfig)
	for _, channel := range channels {
		if channel.Type == constant.ChannelTypeAdvancedCustom {
			advancedConfigs[channel.Id] = channel.GetOtherSettings().AdvancedCustom
		}
	}

	filtered := make([]Ability, 0, len(abilities))
	for _, ability := range abilities {
		config, isAdvancedCustom := advancedConfigs[ability.ChannelId]
		if !isAdvancedCustom {
			filtered = append(filtered, ability)
			continue
		}
		if config != nil && config.SupportsPathForModel(requestPath, model) {
			filtered = append(filtered, ability)
		}
	}
	return filtered
}

func (channel *Channel) AddAbilities(tx *gorm.DB) error {
	models_ := strings.Split(channel.Models, ",")
	groups_ := strings.Split(channel.Group, ",")
	abilitySet := make(map[string]struct{})
	abilities := make([]Ability, 0, len(models_))
	for _, model := range models_ {
		for _, group := range groups_ {
			key := group + "|" + model
			if _, exists := abilitySet[key]; exists {
				continue
			}
			abilitySet[key] = struct{}{}
			ability := Ability{
				Group:     group,
				Model:     model,
				ChannelId: channel.Id,
				Enabled:   channel.Status == common.ChannelStatusEnabled,
				Priority:  channel.Priority,
				Weight:    uint(channel.GetWeight()),
				Tag:       channel.Tag,
			}
			abilities = append(abilities, ability)
		}
	}
	if len(abilities) == 0 {
		return nil
	}
	// choose DB or provided tx
	useDB := DB
	if tx != nil {
		useDB = tx
	}
	for _, chunk := range lo.Chunk(abilities, 50) {
		err := useDB.Clauses(clause.OnConflict{DoNothing: true}).Create(&chunk).Error
		if err != nil {
			return err
		}
	}
	return nil
}

func (channel *Channel) DeleteAbilities() error {
	return DB.Where("channel_id = ?", channel.Id).Delete(&Ability{}).Error
}

func deleteAbilitiesWithTx(tx *gorm.DB, channelID int) error {
	if tx == nil {
		return errors.New("ability deletion requires a transaction")
	}
	return tx.Where("channel_id = ?", channelID).Delete(&Ability{}).Error
}

// UpdateAbilities updates abilities of this channel.
// Make sure the channel is completed before calling this function.
func (channel *Channel) UpdateAbilities(tx *gorm.DB) error {
	isNewTx := false
	// 如果没有传入事务，创建新的事务
	if tx == nil {
		tx = DB.Begin()
		if tx.Error != nil {
			return tx.Error
		}
		isNewTx = true
		defer func() {
			if r := recover(); r != nil {
				tx.Rollback()
			}
		}()
	}

	// First delete all abilities of this channel
	err := tx.Where("channel_id = ?", channel.Id).Delete(&Ability{}).Error
	if err != nil {
		if isNewTx {
			tx.Rollback()
		}
		return err
	}

	// Then add new abilities
	models_ := strings.Split(channel.Models, ",")
	groups_ := strings.Split(channel.Group, ",")
	abilitySet := make(map[string]struct{})
	abilities := make([]Ability, 0, len(models_))
	for _, model := range models_ {
		for _, group := range groups_ {
			key := group + "|" + model
			if _, exists := abilitySet[key]; exists {
				continue
			}
			abilitySet[key] = struct{}{}
			ability := Ability{
				Group:     group,
				Model:     model,
				ChannelId: channel.Id,
				Enabled:   channel.Status == common.ChannelStatusEnabled,
				Priority:  channel.Priority,
				Weight:    uint(channel.GetWeight()),
				Tag:       channel.Tag,
			}
			abilities = append(abilities, ability)
		}
	}

	if len(abilities) > 0 {
		for _, chunk := range lo.Chunk(abilities, 50) {
			err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&chunk).Error
			if err != nil {
				if isNewTx {
					tx.Rollback()
				}
				return err
			}
		}
	}

	// 如果是新创建的事务，需要提交
	if isNewTx {
		return tx.Commit().Error
	}

	return nil
}

func UpdateAbilityStatus(channelId int, status bool) error {
	return updateAbilityStatusWithTx(DB, channelId, status)
}

// updateAbilityStatusWithTx is the tx-aware form of UpdateAbilityStatus. It
// writes the enabled column for every ability row of the channel through the
// given transaction so callers can commit it together with the channel status
// and the gateway routing revision bump.
func updateAbilityStatusWithTx(tx *gorm.DB, channelId int, status bool) error {
	return tx.Model(&Ability{}).Where("channel_id = ?", channelId).Select("enabled").Update("enabled", status).Error
}

// updateAbilityStatusByTagWithTx is the tx-aware form of
// UpdateAbilityStatusByTag. It flips the enabled column for every ability
// row carrying the given tag inside the outer transaction so the channel
// status update and the routing revision bump commit together.
func updateAbilityStatusByTagWithTx(tx *gorm.DB, tag string, status bool) error {
	return tx.Model(&Ability{}).Where("tag = ?", tag).Select("enabled").Update("enabled", status).Error
}

// updateAbilityStatusByModelWithTx is the tx-aware form of
// DisableChannelModel. It flips the enabled column for every ability
// row matching both the given channel_id and model_name inside the outer
// transaction. It deliberately does NOT filter on group, so a single
// channel+model pair across all groups has its enabled status flipped —
// a single dead model on an otherwise healthy channel should not cost the
// channel its other models.
func updateAbilityStatusByModelWithTx(tx *gorm.DB, channelID int, modelName string, status bool) error {
	return tx.Model(&Ability{}).Where("channel_id = ? AND model = ?", channelID, modelName).Select("enabled").Update("enabled", status).Error
}

// DisableChannelModel flips the enabled status of every ability row matching
// the given channel_id and model_name inside one MutateGatewayRouting revision,
// so the ability write and the gateway routing revision bump commit atomically.
// A single disabled model on an otherwise healthy channel should not cost the
// channel its other models; this helper spans ALL groups deliberately.
// Returns an error if modelName is empty, as there is nothing specific to disable.
func DisableChannelModel(channelID int, modelName string) error {
	if modelName == "" {
		return fmt.Errorf("model name must not be empty")
	}
	_, err := MutateGatewayRouting(func(tx *gorm.DB) error {
		if err := updateAbilityStatusByModelWithTx(tx, channelID, modelName, false); err != nil {
			return err
		}
		return nil
	})
	return err
}

// UpdateAbilityStatusByTag remains the public convenience wrapper. It
// delegates to the tx-aware form with the shared DB handle so callers that
// are not inside a MutateGatewayRouting transaction keep working.
func UpdateAbilityStatusByTag(tag string, status bool) error {
	return updateAbilityStatusByTagWithTx(DB, tag, status)
}

// updateAbilityByTagWithTx is the tx-aware form of UpdateAbilityByTag. It
// writes the tag/priority/weight columns for every ability row carrying the
// given tag inside the outer transaction.
func updateAbilityByTagWithTx(tx *gorm.DB, tag string, newTag *string, priority *int64, weight *uint) error {
	ability := Ability{}
	if newTag != nil {
		ability.Tag = newTag
	}
	if priority != nil {
		ability.Priority = priority
	}
	if weight != nil {
		ability.Weight = *weight
	}
	return tx.Model(&Ability{}).Where("tag = ?", tag).Updates(ability).Error
}

// UpdateAbilityByTag remains the public convenience wrapper. It delegates to
// the tx-aware form with the shared DB handle.
func UpdateAbilityByTag(tag string, newTag *string, priority *int64, weight *uint) error {
	return updateAbilityByTagWithTx(DB, tag, newTag, priority, weight)
}

// deleteAbilitiesByChannelIDsWithTx deletes every ability row whose
// channel_id is in ids inside the given transaction, so batch channel
// deletion and the routing revision bump commit atomically.
func deleteAbilitiesByChannelIDsWithTx(tx *gorm.DB, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	return tx.Where("channel_id in (?)", ids).Delete(&Ability{}).Error
}

// deleteAbilitiesByTagWithTx deletes every ability row carrying the given tag
// inside the given transaction. Used when channels sharing a tag are removed.
func deleteAbilitiesByTagWithTx(tx *gorm.DB, tag string) error {
	return tx.Where("tag = ?", tag).Delete(&Ability{}).Error
}

// deleteAbilitiesByStatusWithTx deletes every ability row whose channel has
// the given status, inside the given transaction.
func deleteAbilitiesByStatusWithTx(tx *gorm.DB, status int64) error {
	return tx.Where("channel_id IN (SELECT id FROM channels WHERE status = ?)", status).Delete(&Ability{}).Error
}

var fixLock = sync.Mutex{}

func FixAbility() (int, int, error) {
	lock := fixLock.TryLock()
	if !lock {
		return 0, 0, errors.New("已经有一个修复任务在运行中，请稍后再试")
	}
	defer fixLock.Unlock()

	// truncate abilities table
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		err := DB.Exec("DELETE FROM abilities").Error
		if err != nil {
			common.SysLog(fmt.Sprintf("Delete abilities failed: %s", err.Error()))
			return 0, 0, err
		}
	} else {
		err := DB.Exec("TRUNCATE TABLE abilities").Error
		if err != nil {
			common.SysLog(fmt.Sprintf("Truncate abilities failed: %s", err.Error()))
			return 0, 0, err
		}
	}
	var channels []*Channel
	// Find all channels
	err := DB.Model(&Channel{}).Find(&channels).Error
	if err != nil {
		return 0, 0, err
	}
	if len(channels) == 0 {
		return 0, 0, nil
	}
	successCount := 0
	failCount := 0
	for _, chunk := range lo.Chunk(channels, 50) {
		ids := lo.Map(chunk, func(c *Channel, _ int) int { return c.Id })
		// Delete all abilities of this channel
		err = DB.Where("channel_id IN ?", ids).Delete(&Ability{}).Error
		if err != nil {
			common.SysLog(fmt.Sprintf("Delete abilities failed: %s", err.Error()))
			failCount += len(chunk)
			continue
		}
		// Then add new abilities
		for _, channel := range chunk {
			err = channel.AddAbilities(nil)
			if err != nil {
				common.SysLog(fmt.Sprintf("Add abilities for channel %d failed: %s", channel.Id, err.Error()))
				failCount++
			} else {
				successCount++
			}
		}
	}
	InitChannelCache()
	return successCount, failCount, nil
}
