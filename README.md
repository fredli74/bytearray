# bytearray

Package bytearray provides a fixed size chunk []byte array with slab allocation.
ByteArrays utilize next-chunk-linking which provides full reader/writer
compatibility with any size you need. It uses an automatic chunk allocation with
manual deallocation, which means that all ByteArrays must be manually released
when not used anymore. The slab and chunk sizes are globally configurable and
must be done before any chunks are allocated.

[![Build Status](https://semaphoreci.com/api/v1/fredli74/bytearray/branches/master/badge.svg)](https://semaphoreci.com/fredli74/bytearray)

## Usage

    import "github.com/fredli74/bytearray"

### Example
```go
	// Setup bytearray sizes to 2048 bytes (2KiB) chunks, 4MiB memory slabs and limit to 4096 slabs (16GiB memory)
	// These are global values and can only be setup once before anything is allocated
	bytearray.Setup(2048, 2048 * 2048, 4096)

	// Enable automatic garbage collect to run every 60 seconds and remove anything slab that has had no allocations for at least 120 seconds
	bytearray.EnableAutoGC(60, 120)

	// Open a file
	f, err := os.Open("input.txt")

	// Declare a byte array variable
	var ba ByteArray

	// Copy file content to the byte array
	io.Copy(ba, f)

	// Print size of array
	fmt.Printf("Size is %v", ba.Len())

	// Print debug memory stats
	fmt.Printf("Stats: %v", bytearray.Stats())
	
	// Release the array (important, this is never done automatically, if a variable falls out of scope before it has been Released, the data will still remain in the slab)
	ba.Release()
```

### Reference

```go
const (
	SEEK_SET int = os.SEEK_SET // seek relative to the origin of the array
	SEEK_CUR int = os.SEEK_CUR // seek relative to the current offset
	SEEK_END int = os.SEEK_END // seek relative to the end of the array
)
```

```go
var ChunkSize int = 2048
```
ChunkSize defaults to 2048 bytes (2KiB) chunks. Use Setup function to change.

```go
var MaxSlabs int = 4097 // +1 because slab 0 is never used (or allocated), it is reserved for emptyLocation pointers

```
MaxSlabs defaults to 4096 slabs (16GiB memory with default SlabSize and
ChunkSize). Use Setup function to change.

```go
var SlabSize int = ChunkSize * 2048
```
SlabSize defaults to 2048 slabs (4MiB memory with default ChunkSize). Use Setup
function to change.

#### func  ChunkQuantize

```go
func ChunkQuantize(size int64) int64
```
ChunkQuantize takes a size and round it up to an even chunk size (this can be
used to calculate used memory)

#### func  DisableAutoGC

```go
func DisableAutoGC()
```
DisableAutoGC disables the automatic GC goroutine

#### func  EnableAutoGC

```go
func EnableAutoGC(releaseInterval int, maxAge int)
```
EnableAutoGC enables the automatic GC goroutine releaseInterval is how often the
GC is run. maxAge is the number of seconds since the slab was touched before it
can be freed

#### func  GC

```go
func GC(maxAge int)
```
Not really a GC, more of a slab releaser in case it has not been used for a
while. maxAge is the number of seconds since the slab was touched before it can
be freed

#### func  Setup

```go
func Setup(chunkSize int, slabSize int, maxSlabs int)
```
Setup the ByteArray global sizes. IMPORTANT, Setup can only be called before any
chunks have been allocated or it will throw a panic.

#### func  Stats

```go
func Stats() (AllocatedSlabs int64, GrabbedChunks int64, ReleasedChunks int64, MemoryAllocated int64, MemoryInUse int64)
```
Stats returns the current allocation statistics

#### type ByteArray

```go
type ByteArray struct {
}
```

Byte array read and write is not concurrency safe however the underlying slab
structures are so you can use multiple ByteArrays at the same time

#### func (ByteArray) Len

```go
func (b ByteArray) Len() int
```
Len returns the current length of the ByteArray

#### func (*ByteArray) Read

```go
func (b *ByteArray) Read(p []byte) (n int, err error)
```
Read from the byte array into a buffer and advance the current read position

#### func (*ByteArray) ReadFrom

```go
func (b *ByteArray) ReadFrom(r io.Reader) (n int64, err error)
```
ReadFrom reads from the Reader until EOF and fills up the ByteArray (at the
current write position)

#### func (ByteArray) ReadPosition

```go
func (b ByteArray) ReadPosition() int
```
ReadPosition returns the current write position

#### func (*ByteArray) ReadSeek

```go
func (b *ByteArray) ReadSeek(offset int, whence int) (absolute int, err error)
```
ReadSeek will check bounds and return EOF error if seeking outside

#### func (*ByteArray) ReadSlice

```go
func (b *ByteArray) ReadSlice() ([]byte, error)
```
ReadSlice returns a byte slice chunk for the current read position (it does not
advance read position)

#### func (*ByteArray) Release

```go
func (b *ByteArray) Release()
```
Release will release all chunks associated with the ByteArray

#### func (*ByteArray) Split

```go
func (b *ByteArray) Split(offset int) (newArray ByteArray)
```
Split a byte array into a new ByteArray at the specified offset, the old byte
array will be truncated at the split offset

#### func (*ByteArray) Truncate

```go
func (b *ByteArray) Truncate(offset int) int
```
Truncate sets the length, it also expands the length in case offset > usedBytes

#### func (*ByteArray) Write

```go
func (b *ByteArray) Write(p []byte) (n int, err error)
```
Write to the byte array from a buffer and advance the current write position

#### func (ByteArray) WritePosition

```go
func (b ByteArray) WritePosition() int
```
WritePosition returns the current write position

#### func (*ByteArray) WriteSeek

```go
func (b *ByteArray) WriteSeek(offset int, whence int) int
```
WriteSeek will allocate and expand bounds if needed

#### func (*ByteArray) WriteSlice

```go
func (b *ByteArray) WriteSlice() []byte
```
WriteSlice returns a byte slice chunk for the current write position (it does
not advance write position)

#### func (*ByteArray) WriteTo

```go
func (b *ByteArray) WriteTo(w io.Writer) (n int64, err error)
```
WriteTo writes all ByteArray data (from the current read position) to a writer
interface
