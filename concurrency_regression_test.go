package bloom

import (
	"encoding/binary"
	"sync"
	"sync/atomic"
	"testing"
)

func concurrentRegressionKey(i uint32) []byte {
	key := make([]byte, 4)
	binary.BigEndian.PutUint32(key, i)
	return key
}

func TestConcurrentRegressionAddBatch(t *testing.T) {
	const rounds = 8
	const goroutines = 16
	const keyCount = 512

	for round := 0; round < rounds; round++ {
		f := New(256, 6)
		keys := make([][]byte, keyCount)
		for i := range keys {
			keys[i] = concurrentRegressionKey(uint32(round*keyCount + i))
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for _, key := range keys {
					f.Add(key)
				}
			}()
		}
		close(start)
		wg.Wait()

		for i, key := range keys {
			if !f.Test(key) {
				t.Fatalf("round %d key %d (%x) was lost", round, i, key)
			}
		}
	}
}

func TestConcurrentRegressionMixedOperations(t *testing.T) {
	const goroutines = 12
	const iterations = 300

	f := New(4096, 8)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				key := concurrentRegressionKey(uint32(g*iterations + i))
				switch i % 3 {
				case 0:
					f.Add(key)
					if !f.Test(key) {
						t.Errorf("added key %d is not visible", g*iterations+i)
						return
					}
				case 1:
					f.TestAndAdd(key)
					if !f.Test(key) {
						t.Errorf("TestAndAdd key %d is not visible", g*iterations+i)
						return
					}
				default:
					f.TestOrAdd(key)
					if !f.Test(key) {
						t.Errorf("TestOrAdd key %d is not visible", g*iterations+i)
						return
					}
				}
			}
		}(g)
	}

	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				key := concurrentRegressionKey(uint32(1_000_000 + g*iterations + i))
				_ = f.Test(key)
				f.TestAndAdd(key)
				f.TestOrAdd(key)
			}
		}(g)
	}

	close(start)
	wg.Wait()
}

func TestConcurrentRegressionMinimumCapacity(t *testing.T) {
	f := New(0, 0)
	key := []byte("same-key")

	start := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			f.Add(key)
			if !f.Test(key) {
				t.Error("minimum-capacity key was lost")
			}
		}()
	}
	close(start)
	wg.Wait()
}

func TestConcurrentRegressionTestOrAddSingleAbsentResult(t *testing.T) {
	const goroutines = 32

	f := New(1<<20, 8)
	key := []byte("contended-key")
	start := make(chan struct{})
	var absent int32
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if !f.TestOrAdd(key) {
				atomic.AddInt32(&absent, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if absent != 1 {
		t.Fatalf("TestOrAdd reported absent %d times, want 1", absent)
	}
}

func TestConcurrentRegressionZeroHashFunctions(t *testing.T) {
	var f BloomFilter
	key := []byte("key")

	if got := f.Add(key); got != &f {
		t.Fatal("Add did not return the same filter")
	}
	if !f.Test(key) || !f.TestAndAdd(key) || !f.TestOrAdd(key) {
		t.Fatal("zero hash-function filter should report every key present")
	}
	if !f.TestLocations(nil) {
		t.Fatal("empty locations should report true")
	}
}
