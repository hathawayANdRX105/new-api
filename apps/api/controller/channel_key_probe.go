package controller

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
)

func init() {
	model.ProbeChannelKeyFunc = probeChannelKey
}

// probeChannelKey checks one key of a channel with a model-list request, which
// spends no tokens, and reports whether the verdict is conclusive. Only 200 and
// 401/403 are conclusive: a 429, a 5xx, or a transport error says nothing about
// the key, so the regular state machine keeps ownership of that route.
//
// TestChannel is deliberately not used here — it sends a real model request and
// bills tokens, so it stays a manual operator action.
func probeChannelKey(channelID, keyIndex int) (valid bool, decisive bool) {
	channel, err := model.GetChannelById(channelID, true)
	if err != nil || channel == nil {
		return false, false
	}
	keys := channel.GetKeys()
	if keyIndex < 0 || keyIndex >= len(keys) {
		return false, false
	}
	key := strings.TrimSpace(keys[keyIndex])
	if key == "" {
		return false, false
	}

	baseURL := constant.ChannelBaseURLs[channel.Type]
	if channel.GetBaseURL() != "" {
		baseURL = channel.GetBaseURL()
	}
	if baseURL == "" {
		return false, false
	}

	headers, err := buildFetchModelsHeaders(channel, key)
	if err != nil {
		return false, false
	}
	request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/models", baseURL), nil)
	if err != nil {
		return false, false
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
		if strings.EqualFold(name, "Host") {
			request.Host = headers.Get(name)
		}
	}
	client, err := service.NewProxyHttpClient(channel.GetSetting().Proxy)
	if err != nil {
		return false, false
	}
	response, err := client.Do(request)
	if err != nil {
		common.SysLog(fmt.Sprintf("key probe inconclusive: channel_id=%d key_index=%d error=%v", channelID, keyIndex, sanitizeFetchModelsError(err, key)))
		return false, false
	}
	defer service.CloseResponseBodyGracefully(response)

	switch response.StatusCode {
	case http.StatusOK:
		return true, true
	case http.StatusUnauthorized, http.StatusForbidden:
		return false, true
	default:
		return false, false
	}
}
