package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func withChannelModelHealthControllerDB(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	gin.SetMode(gin.TestMode)
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ChannelModelHealth{}))
	model.DB = db
	model.ClearRouteHealthCache()
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		model.ClearRouteHealthCache()
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
}

func TestChannelModelHealthAdminAPI(t *testing.T) {
	withChannelModelHealthControllerDB(t)

	now := time.Now().Unix()
	until := now + 30
	require.NoError(t, model.DB.Create(&model.ChannelModelHealth{
		ChannelId:           71,
		Model:               "gpt-health",
		State:               model.HealthCalm,
		IsolationLevel:      2,
		Until:               &until,
		Version:             1,
		DormantDisableCount: 1,
		LastErrorCode:       "bad_response",
		LastErrorAt:         &now,
		UpdatedAt:           now,
	}).Error)

	t.Run("lists one channel's model matrix", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel/health?channel_id=71", nil)

		GetChannelModelHealth(ctx)

		require.Equal(t, http.StatusOK, recorder.Code)
		var response struct {
			Success bool                    `json:"success"`
			Data    []channelModelHealthRow `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		require.True(t, response.Success)
		require.Len(t, response.Data, 1)
		assert.Equal(t, "gpt-health", response.Data[0].Model)
		assert.Equal(t, model.HealthCalm, response.Data[0].State)
		assert.Greater(t, response.Data[0].RemainingSeconds, int64(0))
		assert.Equal(t, 1, response.Data[0].DormantDisableCount)
	})

	t.Run("disable then recover changes the persisted route", func(t *testing.T) {
		post := func(action string) *httptest.ResponseRecorder {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Params = gin.Params{{Key: "action", Value: action}}
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/health/"+action, strings.NewReader(`{"channel_id":71,"model":"gpt-health"}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			UpdateChannelModelHealth(ctx)
			return recorder
		}

		require.Equal(t, http.StatusOK, post("disable").Code)
		var row model.ChannelModelHealth
		require.NoError(t, model.DB.Where("channel_id = ? AND model = ?", 71, "gpt-health").First(&row).Error)
		assert.Equal(t, model.HealthDisabled, row.State)
		assert.Nil(t, row.Until)

		require.Equal(t, http.StatusOK, post("recover").Code)
		require.NoError(t, model.DB.Where("channel_id = ? AND model = ?", 71, "gpt-health").First(&row).Error)
		assert.Equal(t, model.HealthHealthy, row.State)
		assert.Zero(t, row.IsolationLevel)
		assert.Zero(t, row.DormantDisableCount)
	})

	t.Run("rejects unknown action", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Params = gin.Params{{Key: "action", Value: "unknown"}}
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/health/unknown", strings.NewReader(`{"channel_id":71,"model":"gpt-health"}`))
		ctx.Request.Header.Set("Content-Type", "application/json")

		UpdateChannelModelHealth(ctx)

		var response struct {
			Success bool `json:"success"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		assert.False(t, response.Success)
	})
}
