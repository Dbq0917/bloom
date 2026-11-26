package bloom

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestDelete(t *testing.T) {
	f := New(1000, 4)
	first := []byte("Bess")
	middle := []byte("Jane")
	last := []byte("Emma")

	f.Add(first)
	f.Add(middle)
	f.Add(last)

	if !f.Delete(middle) {
		t.Fatal("Delete returned false for an added key")
	}
	if !f.Test(first) {
		t.Error("first key should remain after deleting the middle key")
	}
	if !f.Test(last) {
		t.Error("last key should remain after deleting the middle key")
	}
}

func TestDeleteStringAndRemoveAlias(t *testing.T) {
	f := New(64, 4)
	key := "Love"
	f.AddString(key)
	if !f.DeleteString(key) {
		t.Fatal("DeleteString returned false for an added key")
	}
	if f.TestString(key) {
		t.Fatal("DeleteString did not remove the only key")
	}

	f.AddString(key)
	if !f.Remove([]byte(key)) {
		t.Fatal("Remove returned false for an added key")
	}
	if f.TestString(key) {
		t.Fatal("Remove did not remove the only key")
	}
}

func TestDeleteOnlyKeyRestoresEmptyFilter(t *testing.T) {
	f := New(1024, 4)
	key := []byte("solo")
	f.Add(key)

	if !f.Delete(key) {
		t.Fatal("Delete returned false for an added key")
	}
	if f.Test(key) {
		t.Error("deleted key should test false when no other references remain")
	}
	if count := f.b.Count(); count != 0 {
		t.Errorf("bitset has %d set bits after deleting the only key", count)
	}
	for i, word := range f.counts {
		if word != 0 {
			t.Errorf("counter word %d is %d, want zero", i, word)
		}
	}
	if f.excess != nil {
		t.Errorf("overflow counts were retained: %v", f.excess)
	}
}

func TestDeleteSharedBitDoesNotRemoveOtherKeys(t *testing.T) {
	f := New(1, 1)
	first := []byte("first")
	middle := []byte("middle")
	last := []byte("last")

	f.Add(first)
	f.Add(middle)
	f.Add(last)

	if !f.Delete(middle) {
		t.Fatal("Delete returned false for an added key")
	}
	if !f.Test(first) {
		t.Error("first key lost a shared bit")
	}
	if !f.Test(last) {
		t.Error("last key lost a shared bit")
	}
	if count := f.b.Count(); count != 1 {
		t.Fatalf("shared bit count is %d, want 1", count)
	}

	if !f.Delete(first) {
		t.Fatal("second Delete returned false")
	}
	if !f.Test(last) {
		t.Error("remaining key lost a shared bit")
	}
	if !f.Delete(last) {
		t.Fatal("third Delete returned false")
	}
	if f.Test(last) {
		t.Error("key should be absent after its last reference was removed")
	}
	if count := f.b.Count(); count != 0 {
		t.Errorf("bitset has %d set bits after all deletes", count)
	}
}

func TestDeleteRestoresFilterWithoutKey(t *testing.T) {
	parameters := []struct {
		m uint
		k uint
	}{
		{1, 1},
		{1, 4},
		{2, 8},
		{64, 4},
		{1000, 4},
	}

	for _, parameter := range parameters {
		t.Run(fmt.Sprintf("m=%d,k=%d", parameter.m, parameter.k), func(t *testing.T) {
			keys := make([][]byte, 32)
			for i := range keys {
				keys[i] = []byte(fmt.Sprintf("key-%02d", i))
			}
			middle := len(keys) / 2

			f := New(parameter.m, parameter.k)
			expected := New(parameter.m, parameter.k)
			for i, key := range keys {
				f.Add(key)
				if i != middle {
					expected.Add(key)
				}
			}

			if !f.Delete(keys[middle]) {
				t.Fatal("Delete returned false for an added key")
			}
			if !f.b.Equal(expected.b) {
				t.Error("bitset does not match filter where the key was never added")
			}
			if !reflect.DeepEqual(f.counts, expected.counts) {
				t.Error("packed counters do not match filter where the key was never added")
			}
			if !reflect.DeepEqual(f.excess, expected.excess) {
				t.Error("overflow counters do not match filter where the key was never added")
			}
		})
	}
}

func TestDeleteAfterMergeAndCopy(t *testing.T) {
	f := New(1, 1)
	first := []byte("first")
	second := []byte("second")
	f.Add(first)

	g := New(1, 1)
	g.Add(second)
	if err := f.Merge(g); err != nil {
		t.Fatal(err)
	}
	if !f.Delete(first) {
		t.Fatal("Delete after Merge returned false")
	}
	if !f.Test(second) {
		t.Fatal("merged key lost a shared bit after deleting another key")
	}

	copied := f.Copy()
	if !copied.Delete(second) {
		t.Fatal("Delete after Copy returned false")
	}
	if f.Test(second) != true {
		t.Fatal("deleting from a copy changed the original filter")
	}
	if copied.Test(second) {
		t.Fatal("key remains in copy after Delete")
	}
}

func TestDeleteBinarySerializationPreservesReferences(t *testing.T) {
	f := New(1, 1)
	first := []byte("first")
	middle := []byte("middle")
	last := []byte("last")
	f.Add(first)
	f.Add(middle)
	f.Add(last)
	f.Delete(middle)

	var buffer bytes.Buffer
	written, err := f.WriteTo(&buffer)
	if err != nil {
		t.Fatal(err)
	}

	var g BloomFilter
	read, err := g.ReadFrom(&buffer)
	if err != nil {
		t.Fatal(err)
	}
	if read != written {
		t.Fatalf("read %d bytes, wrote %d", read, written)
	}
	if !g.Delete(first) {
		t.Fatal("Delete after binary decode returned false")
	}
	if !g.Test(last) {
		t.Fatal("decoded filter lost a shared reference")
	}
}

func TestDeleteJSONSerializationPreservesReferences(t *testing.T) {
	f := New(1, 1)
	first := []byte("first")
	middle := []byte("middle")
	last := []byte("last")
	f.Add(first)
	f.Add(middle)
	f.Add(last)
	f.Delete(middle)

	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}

	var g BloomFilter
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	if !g.Delete(first) {
		t.Fatal("Delete after JSON decode returned false")
	}
	if !g.Test(last) {
		t.Fatal("JSON-decoded filter lost a shared reference")
	}
}

func TestReadOldBinaryWithoutDeleteState(t *testing.T) {
	key := []byte("old-key")
	old := New(64, 2)
	old.Add(key)

	var buffer bytes.Buffer
	if err := binary.Write(&buffer, binary.BigEndian, uint64(old.m)); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(&buffer, binary.BigEndian, uint64(old.k)); err != nil {
		t.Fatal(err)
	}
	bitBytes, err := old.b.WriteTo(&buffer)
	if err != nil {
		t.Fatal(err)
	}

	var g BloomFilter
	read, err := g.ReadFrom(&buffer)
	if err != nil {
		t.Fatal(err)
	}
	wantRead := int64(16) + bitBytes
	if read != wantRead {
		t.Fatalf("read %d bytes, want %d", read, wantRead)
	}
	if !g.Test(key) {
		t.Fatal("old filter key did not survive read")
	}
	if !g.Delete(key) {
		t.Fatal("Delete on adopted old filter returned false")
	}
	if g.Test(key) {
		t.Fatal("old filter key was not removed")
	}
}

func TestEnsureCountsIgnoresBackingBitsBeyondCap(t *testing.T) {
	f := FromWithM([]uint64{^uint64(0), ^uint64(0)}, 64, 1)
	f.ensureCounts()

	if got := len(f.counts); got != 2 {
		t.Fatalf("counter words = %d, want 2", got)
	}
	for pos := uint(0); pos < 64; pos++ {
		if got := f.exactCounter(pos); got != 1 {
			t.Fatalf("counter at %d = %d, want 1", pos, got)
		}
	}
}

func BenchmarkDelete(b *testing.B) {
	f := NewWithEstimates(uint(b.N), 0.0001)
	key := make([]byte, 8)
	for i := 0; i < b.N; i++ {
		binary.BigEndian.PutUint64(key, uint64(i))
		f.Add(key)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		binary.BigEndian.PutUint64(key, uint64(i))
		f.Delete(key)
	}
}
