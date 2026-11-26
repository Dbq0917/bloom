package bloom

import (
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

const (
	counterBits        = 2
	countersPerWord    = 64 / counterBits
	saturatedCounter   = (1 << counterBits) - 1
	deleteStateVersion = "BloomDL1"
)

var deleteStateMagic = [8]byte{'B', 'l', 'o', 'o', 'm', 'D', 'L', '1'}

func counterWords(m uint) uint {
	words := m / countersPerWord
	if m%countersPerWord != 0 {
		words++
	}
	return words
}

func (f *BloomFilter) ensureCounts() {
	if f.counts != nil {
		return
	}

	f.counts = make([]uint64, counterWords(f.m))
	if f.b == nil {
		return
	}

	next := uint(0)
	for {
		pos, ok := f.b.NextSet(next)
		if !ok || pos >= f.m {
			return
		}
		f.setPackedCounter(pos, 1)
		if pos == ^uint(0) {
			return
		}
		next = pos + 1
	}
}

func counterPlacement(pos uint) (index uint, shift uint) {
	return pos / countersPerWord, counterBits * (pos % countersPerWord)
}

func (f *BloomFilter) packedCounter(pos uint) uint64 {
	index, shift := counterPlacement(pos)
	return (f.counts[index] >> shift) & saturatedCounter
}

func (f *BloomFilter) setPackedCounter(pos uint, value uint64) {
	index, shift := counterPlacement(pos)
	mask := uint64(saturatedCounter) << shift
	f.counts[index] = (f.counts[index] &^ mask) | (value << shift)
}

func (f *BloomFilter) exactCounter(pos uint) uint64 {
	value := f.packedCounter(pos)
	if value != saturatedCounter {
		return value
	}
	return value + f.excess[pos]
}

func (f *BloomFilter) setCounterValue(pos uint, value uint64) {
	if value < saturatedCounter {
		f.setPackedCounter(pos, value)
		if f.excess != nil {
			delete(f.excess, pos)
			if len(f.excess) == 0 {
				f.excess = nil
			}
		}
		return
	}

	f.setPackedCounter(pos, saturatedCounter)
	extra := value - saturatedCounter
	if extra == 0 {
		if f.excess != nil {
			delete(f.excess, pos)
			if len(f.excess) == 0 {
				f.excess = nil
			}
		}
		return
	}

	if f.excess == nil {
		f.excess = make(map[uint]uint64)
	}
	f.excess[pos] = extra
}

func (f *BloomFilter) incrementCounter(pos uint) {
	index, shift := counterPlacement(pos)
	value := (f.counts[index] >> shift) & saturatedCounter
	if value < saturatedCounter {
		f.counts[index] += 1 << shift
		return
	}

	if f.excess == nil {
		f.excess = make(map[uint]uint64)
	}
	f.excess[pos]++
}

func (f *BloomFilter) decrementCounter(pos uint) bool {
	index, shift := counterPlacement(pos)
	value := (f.counts[index] >> shift) & saturatedCounter
	if value == 0 {
		return true
	}
	if value < saturatedCounter {
		f.counts[index] -= 1 << shift
		return value == 1
	}

	if extra, ok := f.excess[pos]; ok {
		if extra > 1 {
			f.excess[pos] = extra - 1
			return false
		}
		delete(f.excess, pos)
		if len(f.excess) == 0 {
			f.excess = nil
		}
		return false
	}

	f.counts[index] -= 1 << shift
	return false
}

// Delete removes one reference to data from the filter.
//
// Add and Delete are reference-counted per bit: a bit shared by other keys is
// retained until every added key using it is deleted. Delete must be paired
// with an Add call for the same data. It returns false when the recorded
// counters do not contain enough references for data; in that case the filter
// is left unchanged. A false return does not prove that data was never added,
// because Bloom filters and their serialized bit sets do not store key
// identities.
func (f *BloomFilter) Delete(data []byte) bool {
	h := baseHashes(data)
	f.ensureCounts()

	locs := make([]uint, f.k)
	needed := make(map[uint]uint64)
	for i := uint(0); i < f.k; i++ {
		loc := f.location(h, i)
		locs[i] = loc
		needed[loc]++
	}

	for loc, count := range needed {
		if f.exactCounter(loc) < count {
			return false
		}
	}

	for _, loc := range locs {
		if f.decrementCounter(loc) {
			f.b.Clear(loc)
		}
	}
	return true
}

// DeleteString removes one reference to data from the filter. See Delete for
// the reference-counting semantics.
func (f *BloomFilter) DeleteString(data string) bool {
	return f.Delete([]byte(data))
}

// Remove is an alias for Delete.
func (f *BloomFilter) Remove(data []byte) bool {
	return f.Delete(data)
}

// RemoveString is an alias for DeleteString.
func (f *BloomFilter) RemoveString(data string) bool {
	return f.DeleteString(data)
}

func (f *BloomFilter) mergeCounts(g *BloomFilter) {
	f.ensureCounts()
	if g.b == nil {
		return
	}

	next := uint(0)
	for {
		pos, ok := g.b.NextSet(next)
		if !ok || pos >= g.m {
			return
		}

		count := uint64(1)
		if g.counts != nil {
			count = g.exactCounter(pos)
			if count == 0 {
				count = 1
			}
		}
		f.setCounterValue(pos, f.exactCounter(pos)+count)

		if pos == ^uint(0) {
			return
		}
		next = pos + 1
	}
}

func (f *BloomFilter) writeDeleteState(stream io.Writer) (int64, error) {
	f.ensureCounts()

	if _, err := stream.Write(deleteStateMagic[:]); err != nil {
		return 0, err
	}
	if err := binary.Write(stream, binary.BigEndian, uint64(len(f.counts))); err != nil {
		return 8, err
	}
	if err := binary.Write(stream, binary.BigEndian, f.counts); err != nil {
		return 16, err
	}
	if err := binary.Write(stream, binary.BigEndian, uint64(len(f.excess))); err != nil {
		return 16 + int64(len(f.counts))*8, err
	}

	keys := make([]uint, 0, len(f.excess))
	for pos := range f.excess {
		keys = append(keys, pos)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	for _, pos := range keys {
		if err := binary.Write(stream, binary.BigEndian, uint64(pos)); err != nil {
			return f.deleteStateSize(), err
		}
		if err := binary.Write(stream, binary.BigEndian, f.excess[pos]); err != nil {
			return f.deleteStateSize(), err
		}
	}

	return f.deleteStateSize(), nil
}

func (f *BloomFilter) deleteStateSize() int64 {
	return int64(24) + int64(len(f.counts))*8 + int64(len(f.excess))*16
}

func (f *BloomFilter) readDeleteState(stream io.Reader) (int64, error) {
	f.counts = nil
	f.excess = nil

	var magic [8]byte
	n, err := io.ReadFull(stream, magic[:])
	read := int64(n)
	if err == io.EOF {
		return 0, nil
	}
	if err != nil {
		return read, err
	}
	if magic != deleteStateMagic {
		return read, fmt.Errorf("bloom: unsupported delete-state format %q", deleteStateVersion)
	}

	var words uint64
	if err := binary.Read(stream, binary.BigEndian, &words); err != nil {
		return read + 8, err
	}
	read += 8

	if words != uint64(counterWords(f.m)) {
		return read, fmt.Errorf("bloom: invalid delete-state counter length %d", words)
	}

	counts := make([]uint64, uint(words))
	if err := binary.Read(stream, binary.BigEndian, counts); err != nil {
		return read, err
	}
	read += int64(words) * 8

	var excessCount uint64
	if err := binary.Read(stream, binary.BigEndian, &excessCount); err != nil {
		return read + 8, err
	}
	read += 8

	excess := make(map[uint]uint64)
	for i := uint64(0); i < excessCount; i++ {
		var pos, value uint64
		if err := binary.Read(stream, binary.BigEndian, &pos); err != nil {
			return read, err
		}
		read += 8
		if err := binary.Read(stream, binary.BigEndian, &value); err != nil {
			return read, err
		}
		read += 8
		if pos < uint64(f.m) && value > 0 {
			excess[uint(pos)] = value
		}
	}

	f.counts = counts
	if len(excess) == 0 {
		f.excess = nil
	} else {
		f.excess = excess
	}
	return read, nil
}
