package bloom

// Growth policy of a ScalableBloomFilter: each new layer doubles the capacity
// of the previous one, and its target false positive rate is tightened by a
// constant ratio, so the false positive rates of all layers form a
// converging geometric series.
const (
	scalableGrowthFactor     = 2
	scalableTighteningFactor = 0.9
)

// A ScalableBloomFilter is a Bloom filter that grows with the data.
//
// A plain BloomFilter is sized for a fixed number of items: once insertions
// exceed that number, the false positive rate rises quickly, and the only
// remedy is to rebuild a larger filter from the original keys, which the
// filter does not store. A ScalableBloomFilter instead stacks a new, larger
// layer on top whenever the current one is full. Keys inserted earlier stay
// in their original layer and are still found there, so existing keys never
// need to be rehashed into a larger bitmap.
//
// Each layer targets a false positive rate tighter than the previous one by
// a constant factor, so the overall false positive rate is bounded by a
// converging geometric series and does not grow with the number of layers.
// Because each layer doubles the capacity of the previous one, the number of
// layers grows only logarithmically with the number of items, keeping the
// cost of a single query bounded.
//
// The zero value is not usable; use NewScalableBloomFilter.
type ScalableBloomFilter struct {
	layers []*scalableLayer
	count  uint
}

// scalableLayer is one Bloom filter generation of a ScalableBloomFilter.
type scalableLayer struct {
	filter   *BloomFilter
	capacity uint
	count    uint
	fp       float64
}

// NewScalableBloomFilter creates a scalable Bloom filter initially sized for
// about n items, keeping the overall false positive rate close to fp no
// matter how many items are added later. An fp outside (0, 1) is replaced
// with 0.01.
func NewScalableBloomFilter(n uint, fp float64) *ScalableBloomFilter {
	if fp <= 0 || fp >= 1 {
		fp = 0.01
	}
	s := &ScalableBloomFilter{}
	s.addLayer(max(1, n), fp*(1-scalableTighteningFactor))
	return s
}

func (s *ScalableBloomFilter) addLayer(capacity uint, fp float64) {
	m, k := EstimateParameters(capacity, fp)
	s.layers = append(s.layers, &scalableLayer{
		filter:   New(m, k),
		capacity: capacity,
		fp:       fp,
	})
}

// Add data to the filter, growing it by one layer if the current layer is
// full. Returns the filter (allows chaining).
func (s *ScalableBloomFilter) Add(data []byte) *ScalableBloomFilter {
	last := s.layers[len(s.layers)-1]
	if last.count >= last.capacity {
		// Guard against capacity overflow on 32-bit platforms: if the
		// computation wraps around, keep using the last layer rather
		// than adding a tiny one.
		if next := scalableGrowthFactor * last.capacity; next > last.capacity {
			s.addLayer(next, last.fp*scalableTighteningFactor)
			last = s.layers[len(s.layers)-1]
		}
	}
	last.filter.Add(data)
	last.count++
	s.count++
	return s
}

// AddString to the filter, growing it by one layer if the current layer is
// full. Returns the filter (allows chaining).
func (s *ScalableBloomFilter) AddString(data string) *ScalableBloomFilter {
	return s.Add([]byte(data))
}

// Test returns true if the data is in any layer of the filter, false
// otherwise. A key is always found in the layer it was originally added to.
// If true, the result might be a false positive. If false, the data is
// definitely not in the set.
func (s *ScalableBloomFilter) Test(data []byte) bool {
	for i := len(s.layers) - 1; i >= 0; i-- {
		if s.layers[i].filter.Test(data) {
			return true
		}
	}
	return false
}

// TestString returns true if the string is in any layer of the filter, false
// otherwise. If true, the result might be a false positive. If false, the
// data is definitely not in the set.
func (s *ScalableBloomFilter) TestString(data string) bool {
	return s.Test([]byte(data))
}

// Layers returns the number of layers the filter has grown to.
func (s *ScalableBloomFilter) Layers() int {
	return len(s.layers)
}

// Count returns the number of items added to the filter across all layers.
func (s *ScalableBloomFilter) Count() uint {
	return s.count
}
