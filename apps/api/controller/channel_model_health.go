package controller

import (
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// channelModelHealthRow is the admin-facing shape of one isolation row. Until is
// exposed as an absolute unix timestamp plus the remaining seconds so the UI can
// render a countdown without trusting the browser clock to match the server.
type channelModelHealthRow struct {
	ChannelId           int    `json:"channel_id"`
	KeyIndex            int    `json:"key_index"`
	Model               string `json:"model"`
	State               string `json:"state"`
	IsolationLevel      int    `json:"isolation_level"`
	Until               *int64 `json:"until"`
	RemainingSeconds    int64  `json:"remaining_seconds"`
	DormantDisableCount int    `json:"dormant_disable_count"`
	LastErrorCode       string `json:"last_error_code"`
	LastErrorAt         *int64 `json:"last_error_at"`
	UpdatedAt           int64  `json:"updated_at"`
}

// GetChannelModelHealth lists isolation state. Without a channel_id it returns
// every row, which is the system-wide view; with one it returns that channel's
// per-model matrix.
func GetChannelModelHealth(c *gin.Context) {
	channelID := 0
	if raw := c.Query("channel_id"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			common.ApiErrorMsg(c, "无效的渠道 ID")
			return
		}
		channelID = parsed
	}

	rows, err := model.ListChannelModelHealth(channelID)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	now := time.Now().Unix()
	payload := make([]channelModelHealthRow, 0, len(rows))
	for _, row := range rows {
		// An elapsed window is reported as healthy with zero remaining time: the
		// selectors already treat it that way, and showing a negative countdown
		// would suggest the route is still isolated when it is not.
		state, remaining := row.State, int64(0)
		if row.Until != nil && *row.Until > now {
			remaining = *row.Until - now
		} else if state == model.HealthCalm || state == model.HealthDormant {
			state = model.HealthHealthy
		}
		payload = append(payload, channelModelHealthRow{
			ChannelId:           row.ChannelId,
			KeyIndex:            row.KeyIndex,
			Model:               row.Model,
			State:               state,
			IsolationLevel:      row.IsolationLevel,
			Until:               row.Until,
			RemainingSeconds:    remaining,
			DormantDisableCount: row.DormantDisableCount,
			LastErrorCode:       row.LastErrorCode,
			LastErrorAt:         row.LastErrorAt,
			UpdatedAt:           row.UpdatedAt,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    payload,
	})
}

type channelModelHealthActionRequest struct {
	ChannelId int    `json:"channel_id"`
	KeyIndex  int    `json:"key_index"`
	Model     string `json:"model"`
}

// UpdateChannelModelHealth disables or recovers one route. Recovery clears the
// isolation ladder and the auto-disable counter, so an operator who fixed the
// upstream does not have to wait out a dormant window.
func UpdateChannelModelHealth(c *gin.Context) {
	var req channelModelHealthActionRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.ChannelId <= 0 || req.Model == "" {
		common.ApiErrorMsg(c, "参数错误")
		return
	}

	action := c.Param("action")
	key := model.RouteKey{ChannelId: req.ChannelId, KeyIndex: req.KeyIndex, Model: req.Model}
	now := time.Now()

	var err error
	switch action {
	case "disable":
		err = model.DisableRoute(key, now)
	case "recover":
		err = model.RecoverRoute(key, now)
	default:
		common.ApiErrorMsg(c, "未知操作")
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
	})
}
