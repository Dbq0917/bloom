package bloom

import (
	"sync"
	"sync/atomic"
)

const bloomKeyLockCount = 1024

var bloomKeyLocks [bloomKeyLockCount]sync.RWMutex

func bloomKeyLock(firstLocation uint) *sync.RWMutex {
	return &bloomKeyLocks[firstLocation&(bloomKeyLockCount-1)]
}

func (f *BloomFilter) keyLock(h [4]uint64) *sync.RWMutex {
	return bloomKeyLock(f.location(h, 0))
}

func bloomLocationsKeyLock(m uint, locations []uint64) *sync.RWMutex {
	if len(locations) == 0 || m == 0 {
		return nil
	}
	return bloomKeyLock(uint(locations[0] % uint64(m)))
}

func bloomTestBit(words []uint64, location uint) bool {
	wordIndex := uint64(location >> 6)
	if wordIndex >= uint64(len(words)) {
		return false
	}
	mask := uint64(1) << (location & 63)
	return atomic.LoadUint64(&words[int(wordIndex)])&mask != 0
}

func bloomSetBit(f *BloomFilter, words []uint64, location uint) {
	wordIndex := uint64(location >> 6)
	if wordIndex >= uint64(len(words)) {
		f.b.Set(location)
		return
	}

	word := &words[int(wordIndex)]
	mask := uint64(1) << (location & 63)
	for {
		old := atomic.LoadUint64(word)
		if old&mask != 0 {
			return
		}
		if atomic.CompareAndSwapUint64(word, old, old|mask) {
			return
		}
	}
}
