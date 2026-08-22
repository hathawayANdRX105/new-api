package model

// Channel selection picks a channel, and only afterwards does
// SetupContextForSelectedChannel pick one of its keys. The isolation state,
// however, lives per (channel, key, model) unit. These helpers collapse the per
// key states of one channel into the single number the selectors need, so a
// multi-key channel is judged by how many of its keys can still serve the model
// rather than by key 0 alone.

// channelKeyCount reports how many keys a channel serves requests with. A single
// key channel is one unit, which keeps its RouteKey at KeyIndex 0.
func channelKeyCount(multiKeySize int) int {
	if multiKeySize < 1 {
		return 1
	}
	return multiKeySize
}

// channelRouteSelectable reports whether any key of the channel may still serve
// the model. Only a channel whose every key is disabled drops out of the
// candidate set; calm and dormant keys stay in at reduced weight (Wave C).
func channelRouteSelectable(channelID, keyCount int, model string) bool {
	for idx := range channelKeyCount(keyCount) {
		if IsRouteSelectable(RouteKey{ChannelId: channelID, KeyIndex: idx, Model: model}) {
			return true
		}
	}
	return false
}

// channelRouteWeightFactor averages the per key weight multipliers of one
// channel for the model. A fully healthy channel returns 1 whatever its key
// count, so adding keys never inflates a channel's traffic share; a channel with
// half its keys in calm lands between the calm scale and 1.
func channelRouteWeightFactor(channelID, keyCount int, model string) float64 {
	count := channelKeyCount(keyCount)
	var sum float64
	for idx := range count {
		sum += RouteWeightMultiplier(RouteKey{ChannelId: channelID, KeyIndex: idx, Model: model})
	}
	return sum / float64(count)
}

// channelKeyCounts loads the key count of each candidate channel in one query
// for the DB selection path, which holds no channel cache to read from.
func channelKeyCounts(channelIDs []int) map[int]int {
	counts := make(map[int]int, len(channelIDs))
	if len(channelIDs) == 0 {
		return counts
	}
	var channels []*Channel
	if err := DB.Select("id", "channel_info").Where("id IN ?", channelIDs).Find(&channels).Error; err != nil {
		// A failed lookup must not empty the candidate pool: fall back to one
		// unit per channel, which is the single-key shape.
		return counts
	}
	for _, channel := range channels {
		counts[channel.Id] = channelKeyCount(channel.ChannelInfo.MultiKeySize)
	}
	return counts
}
