/*
Package bloom provides data structures and methods for creating Bloom filters.

A Bloom filter is a representation of a set of _n_ items, where the main
requirement is to make membership queries; _i.e._, whether an item is a
member of a set.

A Bloom filter has two parameters: _m_, a maximum size (typically a reasonably large
multiple of the cardinality of the set to represent) and _k_, the number of hashing
functions on elements of the set. (The actual hashing functions are important, too,
but this is not a parameter for this implementation). A Bloom filter is backed by
a BitSet; a key is represented in the filter by setting the bits at each value of the
hashing functions (modulo _m_). Set membership is done by _testing_ whether the
bits at each value of the hashing functions (again, modulo _m_) are set. If so,
the item is in the set. If the item is actually in the set, a Bloom filter will
never fail (the true positive rate is 1.0); but it is susceptible to false
positives. The art is to choose _k_ and _m_ correctly.

In this implementation, the hashing functions used is murmurhash,
a non-cryptographic hashing function.

This implementation accepts keys for setting as testing as []byte. Thus, to
add a string item, "Love":

	uint n = 1000
	filter := bloom.New(20*n, 5) // load of 20, 5 keys
	filter.Add([]byte("Love"))

Similarly, to test if "Love" is in bloom:

	if filter.Test([]byte("Love"))

For numeric data, I recommend that you look into the binary/encoding library. But,
for example, to add a uint32 to the filter:

	i := uint32(100)
	n1 := make([]byte,4)
	binary.BigEndian.PutUint32(n1,i)
	f.Add(n1)

Finally, there is a method to estimate the false positive rate of a
Bloom filter with _m_ bits and _k_ hashing functions for a set of size _n_:

	if bloom.EstimateFalsePositiveRate(20*n, 5, n) > 0.001 ...

You can use it to validate the computed m, k parameters:

	m, k := bloom.EstimateParameters(n, fp)
	ActualfpRate := bloom.EstimateFalsePositiveRate(m, k, n)

or

	f := bloom.NewWithEstimates(n, fp)
	ActualfpRate := bloom.EstimateFalsePositiveRate(f.m, f.k, n)

You would expect ActualfpRate to be close to the desired fp in these cases.

The EstimateFalsePositiveRate function creates a temporary Bloom filter. It is
also relatively expensive and only meant for validation.
*/
package bloom

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"

	"github.com/bits-and-blooms/bitset"
)

// A BloomFilter is a representation of a set of _n_ items, where the main
// requirement is to make membership queries; _i.e._, whether an item is a
// member of a set.
type BloomFilter struct {
	m uint
	k uint
	b *bitset.BitSet
	// extra holds filters that could not be combined with this one because
	// their parameters differ. It is nil for ordinary filters.
	extra *mergedFilters
}

// mergedFilters holds Bloom filters with parameters different from the filter
// they are attached to. Queries test all of them, so keys from every merged
// filter remain visible without rehashing.
type mergedFilters struct {
	filters []*BloomFilter
}

func (f *BloomFilter) extraFilters() []*BloomFilter {
	if f.extra == nil {
		return nil
	}
	return f.extra.filters
}

func max(x, y uint) uint {
	if x > y {
		return x
	}
	return y
}

// New creates a new Bloom filter with _m_ bits and _k_ hashing functions
// We force _m_ and _k_ to be at least one to avoid panics.
func New(m uint, k uint) *BloomFilter {
	return &BloomFilter{m: max(1, m), k: max(1, k), b: bitset.New(m)}
}

// From creates a new Bloom filter with len(_data_) * 64 bits and _k_ hashing
// functions. The data slice is not going to be reset.
func From(data []uint64, k uint) *BloomFilter {
	m := uint(len(data) * 64)
	return FromWithM(data, m, k)
}

// FromWithM creates a new Bloom filter with _m_ length, _k_ hashing functions.
// The data slice is not going to be reset.
func FromWithM(data []uint64, m, k uint) *BloomFilter {
	return &BloomFilter{m: m, k: k, b: bitset.From(data)}
}

// baseHashes returns the four hash values of data that are used to create k
// hashes
func baseHashes(data []byte) [4]uint64 {
	var d digest128 // murmur hashing
	hash1, hash2, hash3, hash4 := d.sum256(data)
	return [4]uint64{
		hash1, hash2, hash3, hash4,
	}
}

// location returns the ith hashed location using the four base hash values
func location(h [4]uint64, i uint) uint64 {
	ii := uint64(i)
	return h[ii%2] + ii*h[2+(((ii+(ii%2))%4)/2)]
}

// location returns the ith hashed location using the four base hash values
func (f *BloomFilter) location(h [4]uint64, i uint) uint {
	return uint(location(h, i) % uint64(f.m))
}

// EstimateParameters estimates requirements for m and k.
// Based on https://bitbucket.org/ww/bloom/src/829aa19d01d9/bloom.go
// used with permission.
func EstimateParameters(n uint, p float64) (m uint, k uint) {
	m = uint(math.Ceil(-1 * float64(n) * math.Log(p) / math.Pow(math.Log(2), 2)))
	k = uint(math.Ceil(math.Log(2) * float64(m) / float64(n)))
	return
}

// NewWithEstimates creates a new Bloom filter for about n items with fp
// false positive rate
func NewWithEstimates(n uint, fp float64) *BloomFilter {
	m, k := EstimateParameters(n, fp)
	return New(m, k)
}

// Cap returns the capacity, _m_, of a Bloom filter. If the filter contains
// parts merged with different parameters, Cap reports the receiver's
// original capacity; each part retains its own.
func (f *BloomFilter) Cap() uint {
	return f.m
}

// K returns the number of hash functions used in the BloomFilter. If the
// filter contains parts merged with different parameters, K reports the
// receiver's original value; each part retains its own.
func (f *BloomFilter) K() uint {
	return f.k
}

// BitSet returns the underlying bitset for this filter. If the filter contains
// parts merged with different parameters, this is the receiver's bitset; the
// parts are kept separately.
func (f *BloomFilter) BitSet() *bitset.BitSet {
	return f.b
}

// Add data to the Bloom Filter. Returns the filter (allows chaining)
func (f *BloomFilter) Add(data []byte) *BloomFilter {
	h := baseHashes(data)
	for i := uint(0); i < f.k; i++ {
		f.b.Set(f.location(h, i))
	}
	return f
}

// Merge adds all the keys of g to f.
//
// Filters with the same m and k are combined by a bitwise union. Filters with
// different parameters cannot be combined that way without losing keys, so
// they are retained as additional parts and tested in turn. This is
// equivalent to inserting both sets of keys: keys present in either filter
// are always found. The false positive rate of the result is approximately
// the combination of the rates of its parts (1 - prod(1-p_i), at most their
// sum) rather than the rate of either input alone. Memory use is the sum of
// the parts, not their product, and is proportional to the number of distinct
// (m, k) pairs.
func (f *BloomFilter) Merge(g *BloomFilter) error {
	if g == nil {
		return fmt.Errorf("cannot merge a nil Bloom filter")
	}
	if f == g {
		return nil
	}
	for _, part := range g.allParts() {
		f.absorb(part)
	}
	return nil
}

// allParts returns f followed by every filter merged into it, flattening
// nested merges.
func (f *BloomFilter) allParts() []*BloomFilter {
	parts := []*BloomFilter{f}
	if f.extra != nil {
		for _, part := range f.extra.filters {
			parts = append(parts, part.allParts()...)
		}
	}
	return parts
}

// absorb adds part to f, combining bitsets when their parameters match.
func (f *BloomFilter) absorb(part *BloomFilter) {
	if f.m == part.m && f.k == part.k {
		f.b.InPlaceUnion(part.b)
		return
	}
	if f.extra != nil {
		for _, existing := range f.extra.filters {
			if existing.m == part.m && existing.k == part.k {
				existing.b.InPlaceUnion(part.b)
				return
			}
		}
	}
	if f.extra == nil {
		f.extra = &mergedFilters{}
	}
	f.extra.filters = append(f.extra.filters, part.cloneSelf())
}

// clone returns a deep copy of f, including any merged parts.
func (f *BloomFilter) clone() *BloomFilter {
	fc := f.cloneSelf()
	if f.extra != nil {
		fc.extra = &mergedFilters{filters: make([]*BloomFilter, 0, len(f.extra.filters))}
		for _, part := range f.extra.filters {
			fc.extra.filters = append(fc.extra.filters, part.clone())
		}
	}
	return fc
}

// cloneSelf returns a deep copy of a single filter, without merged parts.
func (f *BloomFilter) cloneSelf() *BloomFilter {
	fc := &BloomFilter{m: f.m, k: f.k}
	if f.b != nil {
		fc.b = f.b.Clone()
	}
	return fc
}

// Copy creates a copy of a Bloom filter.
func (f *BloomFilter) Copy() *BloomFilter {
	return f.clone()
}

// AddString to the Bloom Filter. Returns the filter (allows chaining)
func (f *BloomFilter) AddString(data string) *BloomFilter {
	return f.Add([]byte(data))
}

// Test returns true if the data is in the BloomFilter, false otherwise.
// If true, the result might be a false positive. If false, the data
// is definitely not in the set.
func (f *BloomFilter) Test(data []byte) bool {
	return f.testHashes(baseHashes(data))
}

func (f *BloomFilter) testHashes(h [4]uint64) bool {
	for i := uint(0); i < f.k; i++ {
		if !f.b.Test(f.location(h, i)) {
			if f.extra != nil {
				for _, part := range f.extra.filters {
					if part.testHashes(h) {
						return true
					}
				}
			}
			return false
		}
	}
	return true
}

// TestString returns true if the string is in the BloomFilter, false otherwise.
// If true, the result might be a false positive. If false, the data
// is definitely not in the set.
func (f *BloomFilter) TestString(data string) bool {
	return f.Test([]byte(data))
}

// TestLocations returns true if all locations are set in the BloomFilter, false
// otherwise.
func (f *BloomFilter) TestLocations(locs []uint64) bool {
	if f.extra == nil {
		return f.testLocationsSelf(locs)
	}
	return f.testLocationsMerged(locs)
}

func (f *BloomFilter) testLocationsMerged(locs []uint64) bool {
	n := len(locs)
	if uint(n) > f.k {
		n = int(f.k)
	}
	for i := 0; i < n; i++ {
		if !f.b.Test(uint(locs[i] % uint64(f.m))) {
			if f.extra != nil {
				for _, part := range f.extra.filters {
					if part.testLocationsMerged(locs) {
						return true
					}
				}
			}
			return false
		}
	}
	return true
}

func (f *BloomFilter) testLocationsSelf(locs []uint64) bool {
	for i := 0; i < len(locs); i++ {
		if !f.b.Test(uint(locs[i] % uint64(f.m))) {
			return false
		}
	}
	return true
}

// TestAndAdd is equivalent to calling Test(data) then Add(data).
// The filter is written to unconditionnally: even if the element is present,
// the corresponding bits are still set. See also TestOrAdd.
// Returns the result of Test.
func (f *BloomFilter) TestAndAdd(data []byte) bool {
	h := baseHashes(data)
	present := f.testHashes(h)
	for i := uint(0); i < f.k; i++ {
		l := f.location(h, i)
		f.b.Set(l)
	}
	return present
}

// TestAndAddString is the equivalent to calling Test(string) then Add(string).
// The filter is written to unconditionnally: even if the string is present,
// the corresponding bits are still set. See also TestOrAdd.
// Returns the result of Test.
func (f *BloomFilter) TestAndAddString(data string) bool {
	return f.TestAndAdd([]byte(data))
}

// TestOrAdd is equivalent to calling Test(data) then if not present Add(data).
// If the element is already in the filter, then the filter is unchanged.
// Returns the result of Test.
func (f *BloomFilter) TestOrAdd(data []byte) bool {
	h := baseHashes(data)
	if f.testHashes(h) {
		return true
	}
	for i := uint(0); i < f.k; i++ {
		l := f.location(h, i)
		f.b.Set(l)
	}
	return false
}

// TestOrAddString is the equivalent to calling Test(string) then if not present Add(string).
// If the string is already in the filter, then the filter is unchanged.
// Returns the result of Test.
func (f *BloomFilter) TestOrAddString(data string) bool {
	return f.TestOrAdd([]byte(data))
}

// ClearAll clears all the data in a Bloom filter, removing all keys
func (f *BloomFilter) ClearAll() *BloomFilter {
	f.b.ClearAll()
	if f.extra != nil {
		for _, part := range f.extra.filters {
			part.ClearAll()
		}
	}
	return f
}

// EstimateFalsePositiveRate returns, for a BloomFilter of m bits
// and k hash functions, an estimation of the false positive rate when
//
//	storing n entries. This is an empirical, relatively slow
//
// test using integers as keys.
// This function is useful to validate the implementation.
func EstimateFalsePositiveRate(m, k, n uint) (fpRate float64) {
	rounds := uint32(100000)
	// We construct a new filter.
	f := New(m, k)
	n1 := make([]byte, 4)
	// We populate the filter with n values.
	for i := uint32(0); i < uint32(n); i++ {
		binary.BigEndian.PutUint32(n1, i)
		f.Add(n1)
	}
	fp := 0
	// test for number of rounds
	for i := uint32(0); i < rounds; i++ {
		binary.BigEndian.PutUint32(n1, i+uint32(n)+1)
		if f.Test(n1) {
			fp++
		}
	}
	fpRate = float64(fp) / (float64(rounds))
	return
}

// ApproximatedSize estimates the number of items added to the filter from the
// number of bits set.
// https://en.wikipedia.org/wiki/Bloom_filter#Approximating_the_number_of_items_in_a_Bloom_filter
//
// It returns 0 for an empty filter. It returns math.MaxUint32 when the filter
// is saturated (all bits set) or when the estimate does not fit in a uint32.
// For a filter containing merged parts, the estimates of all parts are summed.
func (f *BloomFilter) ApproximatedSize() uint32 {
	total := uint64(f.approximatedSize())
	if f.extra != nil {
		for _, part := range f.extra.filters {
			size := uint64(part.ApproximatedSize())
			if total > uint64(math.MaxUint32)-size {
				return math.MaxUint32
			}
			total += size
		}
	}
	if total > uint64(math.MaxUint32) {
		return math.MaxUint32
	}
	return uint32(total)
}

func (f *BloomFilter) approximatedSize() uint32 {
	x := float64(f.b.Count())
	if x == 0 {
		return 0
	}
	m := float64(f.Cap())
	k := float64(f.K())
	if x >= m {
		// Saturated filter: return max value since the estimate is infinite.
		return math.MaxUint32
	}
	size := -1 * m / k * math.Log(1-x/m) / math.Log(math.E)
	// Converting NaN or a value out of range to uint32 is platform dependent.
	if math.IsNaN(size) || size > float64(math.MaxUint32) {
		return math.MaxUint32
	}
	return uint32(math.Floor(size + 0.5)) // round
}

// bloomFilterJSON is an unexported type for marshaling/unmarshaling BloomFilter struct.
type bloomFilterJSON struct {
	M     uint              `json:"m"`
	K     uint              `json:"k"`
	B     *bitset.BitSet    `json:"b"`
	Parts []bloomFilterJSON `json:"parts,omitempty"`
}

// MarshalJSON implements json.Marshaler interface.
func (f BloomFilter) MarshalJSON() ([]byte, error) {
	return json.Marshal(f.jsonValue())
}

func (f BloomFilter) jsonValue() bloomFilterJSON {
	j := bloomFilterJSON{M: f.m, K: f.k, B: f.b}
	if f.extra != nil {
		for _, part := range f.extra.filters {
			j.Parts = append(j.Parts, part.jsonValue())
		}
	}
	return j
}

// UnmarshalJSON implements json.Unmarshaler interface.
func (f *BloomFilter) UnmarshalJSON(data []byte) error {
	var j bloomFilterJSON
	err := json.Unmarshal(data, &j)
	if err != nil {
		return err
	}
	*f = *j.filter()
	return nil
}

func (j bloomFilterJSON) filter() *BloomFilter {
	f := &BloomFilter{m: j.M, k: j.K, b: j.B}
	if len(j.Parts) > 0 {
		f.extra = &mergedFilters{filters: make([]*BloomFilter, 0, len(j.Parts))}
		for _, part := range j.Parts {
			f.extra.filters = append(f.extra.filters, part.filter())
		}
	}
	return f
}

// mergedFormatMagic marks the binary representation of a filter that carries
// additional differently-parameterized parts. A normal filter always starts
// with m, which cannot have the high bit set because such a filter could not
// be allocated.
const mergedFormatMagic uint64 = 1<<63 | 0x626c6f6f6d2d756e // "bloom-un"

// WriteTo writes a binary representation of the BloomFilter to an i/o stream.
// It returns the number of bytes written.
//
// Performance: if this function is used to write to a disk or network
// connection, it might be beneficial to wrap the stream in a bufio.Writer.
// E.g.,
//
//	      f, err := os.Create("myfile")
//		       w := bufio.NewWriter(f)
func (f *BloomFilter) WriteTo(stream io.Writer) (int64, error) {
	if f.extra != nil && len(f.extra.filters) > 0 {
		parts := f.allParts()
		written := int64(binary.Size(uint64(0)))
		if err := binary.Write(stream, binary.BigEndian, mergedFormatMagic); err != nil {
			return 0, err
		}
		if err := binary.Write(stream, binary.BigEndian, uint64(len(parts))); err != nil {
			return written, err
		}
		written += int64(binary.Size(uint64(0)))
		for _, part := range parts {
			numBytes, err := part.writeTo(stream)
			written += numBytes
			if err != nil {
				return written, err
			}
		}
		return written, nil
	}
	return f.writeTo(stream)
}

func (f *BloomFilter) writeTo(stream io.Writer) (int64, error) {
	err := binary.Write(stream, binary.BigEndian, uint64(f.m))
	if err != nil {
		return 0, err
	}
	err = binary.Write(stream, binary.BigEndian, uint64(f.k))
	if err != nil {
		return 0, err
	}
	numBytes, err := f.b.WriteTo(stream)
	return numBytes + int64(2*binary.Size(uint64(0))), err
}

// ReadFrom reads a binary representation of the BloomFilter (such as might
// have been written by WriteTo()) from an i/o stream. It returns the number
// of bytes read.
//
// Performance: if this function is used to read from a disk or network
// connection, it might be beneficial to wrap the stream in a bufio.Reader.
// E.g.,
//
//	f, err := os.Open("myfile")
//	r := bufio.NewReader(f)
func (f *BloomFilter) ReadFrom(stream io.Reader) (int64, error) {
	var first uint64
	if err := binary.Read(stream, binary.BigEndian, &first); err != nil {
		return 0, err
	}
	if first != mergedFormatMagic {
		numBytes, err := f.readFrom(stream, first)
		if err != nil {
			return 0, err
		}
		return numBytes + int64(binary.Size(uint64(0))), nil
	}

	var count uint64
	if err := binary.Read(stream, binary.BigEndian, &count); err != nil {
		return int64(binary.Size(uint64(0))), err
	}
	if count == 0 {
		return int64(2 * binary.Size(uint64(0))), fmt.Errorf("invalid merged Bloom filter: no parts")
	}

	total := int64(2 * binary.Size(uint64(0)))
	parts := make([]*BloomFilter, 0)
	for i := uint64(0); i < count; i++ {
		var m uint64
		if err := binary.Read(stream, binary.BigEndian, &m); err != nil {
			return total, err
		}
		total += int64(binary.Size(uint64(0)))
		part := &BloomFilter{}
		numBytes, err := part.readFrom(stream, m)
		total += numBytes
		if err != nil {
			return total, err
		}
		parts = append(parts, part)
	}
	f.m = parts[0].m
	f.k = parts[0].k
	f.b = parts[0].b
	if len(parts) > 1 {
		f.extra = &mergedFilters{filters: parts[1:]}
	} else {
		f.extra = nil
	}
	return total, nil
}

func (f *BloomFilter) readFrom(stream io.Reader, m uint64) (int64, error) {
	var k uint64
	if err := binary.Read(stream, binary.BigEndian, &k); err != nil {
		return 0, err
	}
	b := &bitset.BitSet{}
	numBytes, err := b.ReadFrom(stream)
	if err != nil {
		return 0, err
	}
	f.m = uint(m)
	f.k = uint(k)
	f.b = b
	f.extra = nil
	return numBytes + int64(binary.Size(uint64(0))), nil
}

// GobEncode implements gob.GobEncoder interface.
func (f *BloomFilter) GobEncode() ([]byte, error) {
	var buf bytes.Buffer
	_, err := f.WriteTo(&buf)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// GobDecode implements gob.GobDecoder interface.
func (f *BloomFilter) GobDecode(data []byte) error {
	buf := bytes.NewBuffer(data)
	_, err := f.ReadFrom(buf)

	return err
}

// MarshalBinary implements binary.BinaryMarshaler interface.
func (f *BloomFilter) MarshalBinary() ([]byte, error) {
	var buf bytes.Buffer
	_, err := f.WriteTo(&buf)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// UnmarshalBinary implements binary.BinaryUnmarshaler interface.
func (f *BloomFilter) UnmarshalBinary(data []byte) error {
	buf := bytes.NewBuffer(data)
	_, err := f.ReadFrom(buf)

	return err
}

// Equal tests for the equality of two Bloom filters
func (f *BloomFilter) Equal(g *BloomFilter) bool {
	if f.m != g.m || f.k != g.k || !f.b.Equal(g.b) {
		return false
	}
	fParts := f.extraFilters()
	gParts := g.extraFilters()
	if len(fParts) != len(gParts) {
		return false
	}
	for i := 0; i < len(fParts); i++ {
		if !fParts[i].Equal(gParts[i]) {
			return false
		}
	}
	return true
}

// Locations returns a list of hash locations representing a data item.
func Locations(data []byte, k uint) []uint64 {
	locs := make([]uint64, k)

	// calculate locations
	h := baseHashes(data)
	for i := uint(0); i < k; i++ {
		locs[i] = location(h, i)
	}

	return locs
}
