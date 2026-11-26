package bloom

import (
	"encoding/binary"
	"fmt"
	"testing"
)

func scalableKey(i uint32) []byte {
	key := make([]byte, 4)
	binary.BigEndian.PutUint32(key, i)
	return key
}

// TestScalableBloomFilterNoFalseNegatives inserts far more keys than the
// initial capacity and verifies that every key, including the ones added to
// the earliest layers, is still found.
func TestScalableBloomFilterNoFalseNegatives(t *testing.T) {
	initial := uint(1000)
	s := NewScalableBloomFilter(initial, 0.01)
	n := 100 * initial
	for i := uint32(0); i < uint32(n); i++ {
		s.Add(scalableKey(i))
	}
	if s.Count() != n {
		t.Errorf("Count() = %d, expected %d", s.Count(), n)
	}
	if s.Layers() < 2 {
		t.Errorf("expected the filter to grow beyond one layer, got %d", s.Layers())
	}
	for i := uint32(0); i < uint32(n); i++ {
		if !s.Test(scalableKey(i)) {
			t.Fatalf("key %d not found after %d layers", i, s.Layers())
		}
	}
}

// TestScalableBloomFilterFalsePositiveRate inserts 100 times the initial
// capacity and checks that the measured false positive rate stays close to
// the target instead of growing with the number of layers.
func TestScalableBloomFilterFalsePositiveRate(t *testing.T) {
	initial := uint(10000)
	fp := 0.01
	s := NewScalableBloomFilter(initial, fp)
	n := 100 * initial
	for i := uint32(0); i < uint32(n); i++ {
		s.Add(scalableKey(i))
	}
	rounds := uint32(100000)
	hits := 0
	for i := uint32(0); i < rounds; i++ {
		if s.Test(scalableKey(i + uint32(n) + 1)) {
			hits++
		}
	}
	rate := float64(hits) / float64(rounds)
	t.Logf("layers=%d items=%d fpRate=%.4f target=%.4f", s.Layers(), n, rate, fp)
	if rate > 2*fp {
		t.Errorf("false positive rate %.4f exceeds 2x target %.4f", rate, fp)
	}
	// With doubling layers, 100x growth must need only a handful of layers.
	if s.Layers() > 10 {
		t.Errorf("expected logarithmic layer growth, got %d layers", s.Layers())
	}
}

func TestScalableBloomFilterString(t *testing.T) {
	s := NewScalableBloomFilter(10, 0.01)
	for i := 0; i < 1000; i++ {
		s.AddString(fmt.Sprintf("key-%d", i))
	}
	for i := 0; i < 1000; i++ {
		if !s.TestString(fmt.Sprintf("key-%d", i)) {
			t.Errorf("key-%d not found", i)
		}
	}
	if s.TestString("definitely-not-present") {
		t.Log("false positive on absent key (allowed, unlikely)")
	}
}

func TestScalableBloomFilterInvalidFP(t *testing.T) {
	for _, fp := range []float64{0, -1, 1, 2} {
		s := NewScalableBloomFilter(100, fp)
		s.AddString("a")
		if !s.TestString("a") {
			t.Errorf("fp=%v: added key not found", fp)
		}
	}
}

// BenchmarkScalableBloomFilterTest measures the per-query cost as the number
// of items grows far beyond the initial capacity.
func BenchmarkScalableBloomFilterTest(b *testing.B) {
	initial := uint(10000)
	for _, mult := range []uint{1, 10, 100} {
		s := NewScalableBloomFilter(initial, 0.01)
		n := mult * initial
		for i := uint32(0); i < uint32(n); i++ {
			s.Add(scalableKey(i))
		}
		b.Run(fmt.Sprintf("items=%dxCapacity/layers=%d", mult, s.Layers()), func(b *testing.B) {
			key := make([]byte, 4)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				binary.BigEndian.PutUint32(key, uint32(i)+uint32(n)+1)
				s.Test(key)
			}
		})
	}
}
