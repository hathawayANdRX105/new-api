package model

// routingBaseWeight converts a configured channel weight into the base weight
// used for weighted-random routing. Both selection paths (the memory-cache path
// in GetRandomSatisfiedChannel and the DB path in GetChannel) MUST call this so
// MEMORY_CACHE_ENABLED cannot change traffic distribution.
//
// The +1 offset keeps weight=0 channels selectable at the lowest possible share
// instead of dropping them, while staying strictly monotone: a larger configured
// weight always yields a larger routing weight.
func routingBaseWeight(weight int) uint {
	if weight < 0 {
		return 1
	}
	return uint(weight) + 1
}
