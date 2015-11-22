// Copyright 2015 Fredrik Lidström. All rights reserved.
// Use of this source code is governed by the standard MIT License (MIT)
// that can be found in the LICENSE file.

// Package bytearray provides a fixed size chunk []byte array with slab allocation.
// ByteArrays utilize next-chunk-linking which provides full reader/writer compatibility
// with any size you need. It uses an automatic chunk allocation with manual deallocation,
// which means that all ByteArrays must be manually released when not used anymore to free
// up the memory again. The optional "GC" routine is only in place to free up fully empty
// slabs if they have not been used for a while. The slab and chunk sizes are configurable
// globally and must be done before any chunks are allocated to prevent data corruption.
package bytearray

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"time"
)

//*******************************************************************************//
//                                    public                                     //
//*******************************************************************************//

const (
	SEEK_SET int = os.SEEK_SET // seek relative to the origin of the array
	SEEK_CUR int = os.SEEK_CUR // seek relative to the current offset
	SEEK_END int = os.SEEK_END // seek relative to the end of the array
)

// ChunkSize defaults to 2048 bytes (2KiB) chunks. Use Setup function to change.
var ChunkSize int = 2048

// SlabSize defaults to 2048 slabs (4MiB memory with default ChunkSize). Use Setup function to change.
var SlabSize int = ChunkSize * 2048

// MaxSlabs defaults to 4096 slabs (16GiB memory with default SlabSize and ChunkSize). Use Setup function to change.
var MaxSlabs int = 4097 // +1 because slab 0 is never used (or allocated), it is reserved for emptyLocation pointers

// Setup the ByteArray global sizes. IMPORTANT, Setup can only be called before any
// chunks have been allocated or it will throw a panic.
func Setup(chunkSize int, slabSize int, maxSlabs int) {
	if allocatedSlabs > 0 {
		panic(errors.New("bytearray setup called with slabs already allocated"))
	}
	ChunkSize = chunkSize
	SlabSize = slabSize
	MaxSlabs = maxSlabs + 1             // +1 because slab 0 is never used (or allocated), it is reserved for emptyLocation pointers
	slabs = make([]*byteSlab, MaxSlabs) // Fixed array size because we do not want it to move around in memory, ever!
}

var autoGCstop chan struct{}
var autoGCwg sync.WaitGroup

// EnableAutoGC enables the automatic GC goroutine
// releaseInterval is how often the GC is run.
// maxAge is the number of seconds since the slab was touched before it can be freed
func EnableAutoGC(releaseInterval int, maxAge int) {
	if autoGCstop == nil {
		autoGCstop = make(chan struct{})
		autoGCwg.Add(1)
		go func() {
			for { // ever
				select {
				case <-time.After(time.Duration(releaseInterval) * time.Second):
					GC(maxAge)
				case <-autoGCstop:
					break
				}
			}
			autoGCwg.Done()
		}()
	}
}

// DisableAutoGC disables the automatic GC goroutine
func DisableAutoGC() {
	close(autoGCstop)
	autoGCwg.Wait()
}

// Not really a GC, more of a slab releaser in case it has not been used for a while.
// maxAge is the number of seconds since the slab was touched before it can be freed
func GC(maxAge int) {
	memoryMutex.Lock()
	defer memoryMutex.Unlock()

	if allocatedSlabs > 0 {
		for s := range slabs {
			//if slabs[s] != nil {
			//	fmt.Printf("slab %d, free %d, total %d, touched %.2f sec\n", s, slabs[s].free, len(slabs[s].next), time.Since(slabs[s].touched).Seconds())
			//}
			if slabs[s] != nil && slabs[s].free == len(slabs[s].next) && time.Since(slabs[s].touched).Seconds() >= float64(maxAge) {
				deallocateSlab(uint16(s))
				runtime.GC()
			}
		}
	}
}

// Stats returns the current allocation statistics
func Stats() (AllocatedSlabs int64, GrabbedChunks int64, ReleasedChunks int64, MemoryAllocated int64, MemoryInUse int64) {
	memoryMutex.Lock()
	defer memoryMutex.Unlock()

	AllocatedSlabs = int64(allocatedSlabs)
	GrabbedChunks = grabbedChunks
	ReleasedChunks = releasedChunks
	MemoryInUse = (GrabbedChunks - ReleasedChunks) * int64(ChunkSize)
	MemoryAllocated = AllocatedSlabs * int64(SlabSize)
	return
}

// ChunkQuantize takes a size and round it up to an even chunk size (this can be used to calculate used memory)
func ChunkQuantize(size int) int {
	return ChunkSize + (size/ChunkSize)*ChunkSize
}

//*******************************************************************************//
//                                   ByteArray                                   //
//*******************************************************************************//

// Byte array read and write is not concurrency safe however the underlying slab structures
// are so you can use multiple ByteArrays at the same time
type ByteArray struct {
	rootChunk  uint32   // first chunk of the array
	rootOffset int      // internal offset of the data, used when splitting arrays
	usedBytes  int      // total of used bytes
	writePos   position // current writer position
	readPos    position // current reader position
}

// WritePosition returns the current write position
func (b ByteArray) WritePosition() int {
	return b.writePos.current
}

// ReadPosition returns the current write position
func (b ByteArray) ReadPosition() int {
	return b.readPos.current
}

// Len returns the current length of the ByteArray
func (b ByteArray) Len() int {
	return b.usedBytes
}

// Release will release all chunks associated with the ByteArray
func (b *ByteArray) Release() {
	b.Truncate(0)
	if b.rootChunk != emptyLocation {
		releaseChunk(b.rootChunk)
		b.rootChunk = emptyLocation
	}
}

// Split a byte array into a new ByteArray at the specified offset, the old byte array will be truncated at the split offset
func (b *ByteArray) Split(offset int) (newArray ByteArray) {
	if offset > b.usedBytes {
		panic("ASSERT")
	}
	if b.rootChunk == emptyLocation {
		panic("ASSERT")
	}

	if offset == 0 {
		newArray.rootChunk = b.rootChunk
		b.rootChunk = emptyLocation
	} else if offset == b.usedBytes {
		// optimization because no copy or rootOffset is needed
	} else if (b.rootOffset+offset)%ChunkSize > 0 { // Split inside a chunk
		var splitPosition position
		splitPosition = b.seek(splitPosition, offset, SEEK_SET)
		splitSlice := getSlice(splitPosition)

		newArray.rootOffset = splitPosition.chunkPos
		newArray.prepare()
		newSlice := newArray.WriteSlice()

		// Duplicate the split block
		copy(newSlice, splitSlice)
		setNextLocation(newArray.rootChunk, getNextLocation(splitPosition.chunk))
		setNextLocation(splitPosition.chunk, emptyLocation)
	} else {
		var splitPosition position
		splitPosition = b.seek(splitPosition, offset-1, SEEK_SET) // -1 just to get the index of the previous chunk
		newArray.rootChunk = getNextLocation(splitPosition.chunk)
		setNextLocation(splitPosition.chunk, emptyLocation)
	}
	newArray.usedBytes = b.usedBytes - offset
	newArray.writePos = newArray.seek(newArray.writePos, 0, SEEK_END)
	if newArray.readPos.current != 0 {
		panic("ASSERT!")
	}
	//newArray.readPos = newArray.seek(newArray.readPos, 0, SEEK_SET)
	b.Truncate(offset)
	return
}

// Truncate sets the length, it also expands the length in case offset > usedBytes
func (b *ByteArray) Truncate(offset int) int {
	var p position
	p = b.seek(p, offset, SEEK_SET)
	for next := getNextLocation(p.chunk); next != emptyLocation; {
		releaseMe := next
		next = getNextLocation(next)
		releaseChunk(releaseMe)
	}
	setNextLocation(p.chunk, emptyLocation)
	b.usedBytes = p.current
	if b.readPos.current > b.usedBytes {
		b.readPos = b.seek(b.readPos, 0, SEEK_END)
	}
	if b.writePos.current > b.usedBytes {
		b.writePos = b.seek(b.writePos, 0, SEEK_END)
	}
	return b.usedBytes
}

// WriteSeek will allocate and expand bounds if needed
func (b *ByteArray) WriteSeek(offset int, whence int) int {
	b.writePos = b.seek(b.writePos, offset, whence)
	return b.writePos.current
}

// ReadSeek will check bounds and return EOF error if seeking outside
func (b *ByteArray) ReadSeek(offset int, whence int) (absolute int, err error) {
	switch whence {
	case SEEK_SET:
		absolute = offset
	case SEEK_CUR:
		absolute = b.readPos.current + offset
	case SEEK_END:
		absolute = b.usedBytes - offset
	}
	if absolute < 0 {
		absolute = 0
		err = io.EOF
	}
	if absolute > b.usedBytes {
		absolute = b.usedBytes
		err = io.EOF
	}
	b.readPos = b.seek(b.readPos, absolute, SEEK_SET)
	return b.readPos.current, err
}

// ReadSlice returns a byte slice chunk for the current read position (it does not advance read position)
func (b *ByteArray) ReadSlice() ([]byte, error) {
	if b.readPos.current >= b.usedBytes {
		return nil, io.EOF
	}
	slice := getSlice(b.readPos)
	if len(slice) > b.usedBytes-b.readPos.current {
		return slice[:b.usedBytes-b.readPos.current], nil
	} else {
		return slice, nil
	}
}

// Read from the byte array into a buffer and advance the current read position
func (b *ByteArray) Read(p []byte) (n int, err error) {
	for n = 0; n < len(p); {
		var slice []byte
		slice, err = b.ReadSlice()
		if slice != nil {
			read := copy(p[n:], slice)
			b.readPos = b.seek(b.readPos, read, SEEK_CUR)
			n += read
		} else {
			break
		}
	}
	if n < len(p) {
		err = io.EOF
	}
	return n, err
}

// WriteTo writes all ByteArray data (from the current read position) to a writer interface
func (b *ByteArray) WriteTo(w io.Writer) (n int64, err error) {
	for b.readPos.current < b.usedBytes {
		slice, er := b.ReadSlice()
		if slice != nil {
			read, err := w.Write(slice)
			b.readPos = b.seek(b.readPos, read, SEEK_CUR)
			n += int64(read)
			if err != nil {
				return n, err
			}
		} else {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return n, err
}

// WriteSlice returns a byte slice chunk for the current write position (it does not advance write position)
func (b *ByteArray) WriteSlice() []byte {
	b.prepare()
	return getSlice(b.writePos)
}

// Write to the byte array from a buffer and advance the current write position
func (b *ByteArray) Write(p []byte) (n int, err error) {
	for n = 0; n < len(p); {
		var slice []byte
		slice = b.WriteSlice()
		if slice == nil {
			panic("ASSERT")
		}

		written := copy(slice, p[n:])
		b.writePos = b.seek(b.writePos, written, SEEK_CUR)
		n += written
	}
	return n, err
}

// ReadFrom reads from the Reader until EOF and fills up the ByteArray (at the current write position)
func (b *ByteArray) ReadFrom(r io.Reader) (n int64, err error) {
	for {
		slice := b.WriteSlice()
		if slice == nil {
			panic("ASSERT")
		}

		written, er := r.Read(slice)
		b.writePos = b.seek(b.writePos, written, SEEK_CUR)
		n += int64(written)
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return n, err
}

// internal function to prepare a ByteArray for writing
func (b *ByteArray) prepare() {
	if b.rootChunk == emptyLocation {
		b.rootChunk = grabChunk()
		b.readPos = b.seek(b.readPos, 0, SEEK_SET)
		b.writePos = b.seek(b.writePos, 0, SEEK_SET)
	}
}

// internal seek function (does not limit on size, it will allocate and grow)
func (b *ByteArray) seek(was position, offset int, whence int) (now position) {
	switch whence {
	case SEEK_SET:
		now.current = offset
	case SEEK_CUR:
		now.current = was.current + offset
	case SEEK_END:
		now.current = b.usedBytes - offset
	}
	now.chunkPos = (now.current + b.rootOffset) % ChunkSize
	now.chunkIX = (now.current + b.rootOffset) / ChunkSize
	if was.chunkIX == 0 || now.chunkIX < was.chunkIX { // Chunks are only linked forward, so in reverse we need to restart
		was.chunkIX = 0
		b.prepare()
		was.chunk = b.rootChunk
	}
	now.chunk = was.chunk
	for was.chunkIX < now.chunkIX {
		if now.chunk == emptyLocation {
			panic("ASSERT")
		}
		if getNextLocation(now.chunk) == emptyLocation {
			now.chunk = appendChunk(now.chunk)
		} else {
			now.chunk = getNextLocation(now.chunk)
		}
		was.chunkIX++
	}
	if now.current > b.usedBytes {
		b.usedBytes = now.current
	}
	return now
}

//*******************************************************************************//
//                                   internals                                   //
//*******************************************************************************//

type position struct {
	current  int
	chunkIX  int
	chunkPos int
	chunk    uint32
}

type byteSlab struct {
	memory []byte
	next   []uint32
	used   []bool // Only used for ASSERT checking, should be removed
	free   int

	touched time.Time
}

const emptyLocation uint32 = 0 // Location is empty

var slabs []*byteSlab = make([]*byteSlab, MaxSlabs)
var freeChunk uint32 = emptyLocation
var allocatedSlabs uint32
var grabbedChunks int64
var releasedChunks int64

var memoryMutex sync.Mutex

type byteChunkLocation uint32 // upper 16bit is slabIndex, lower 16bit is chunkIndex
func getChunkLocation(chunk uint32) (slabIndex, chunkIndex uint16) {
	return uint16(chunk >> 16), uint16(chunk)
}
func setChunkLocation(slabIndex, chunkIndex uint16) uint32 {
	return uint32(slabIndex)<<16 | uint32(chunkIndex)
}
func getNextLocation(chunk uint32) uint32 {
	if chunk == emptyLocation {
		return emptyLocation
	} else {
		return slabs[(chunk>>16)&0xffff].next[(chunk & 0xffff)]
	}
}
func setNextLocation(chunk uint32, next uint32) {
	if chunk == emptyLocation {
		panic("ASSERT!")
	}
	slabs[(chunk>>16)&0xffff].next[(chunk & 0xffff)] = next
}

// getSlice gets a byte slice for a chunk position
func getSlice(p position) []byte {
	s, i := getChunkLocation(p.chunk)
	bufStart := int(i)*ChunkSize + p.chunkPos
	bufLen := ChunkSize - p.chunkPos
	return slabs[s].memory[bufStart : bufStart+bufLen]
}

// appendChunk adds a chunk to the chain after the "after" chunk
func appendChunk(after uint32) (newChunk uint32) {
	if getNextLocation(after) != emptyLocation {
		panic("ASSERT!")
	}

	newChunk = grabChunk()
	setNextLocation(newChunk, getNextLocation(after))
	setNextLocation(after, newChunk)
	return newChunk
}

func allocateSlab() (ix uint16) {
	slab := &byteSlab{
		memory: make([]byte, SlabSize),
		next:   make([]uint32, SlabSize/ChunkSize),
		used:   make([]bool, SlabSize/ChunkSize), // Only used for ASSERT checking, should be removed
	}
	ix = 1
	for ; slabs[ix] != nil; ix++ {
	}
	slabs[ix] = slab
	allocatedSlabs++

	for i, _ := range slab.next {
		slabs[ix].used[i] = false
		slabs[ix].free++

		release := setChunkLocation(uint16(ix), uint16(i))
		setNextLocation(release, freeChunk)
		freeChunk = release
	}
	return ix
}
func deallocateSlab(ix uint16) {
	for i := range slabs[ix].used {
		if slabs[ix].used[i] {
			panic("ASSERT: Deallocate on a slab that has a chunk in use")
		}
	}

	this, last := freeChunk, emptyLocation
	freeChunk = emptyLocation
	for ; this != emptyLocation || last != emptyLocation; this = getNextLocation(this) {
		s, _ := getChunkLocation(this)
		if s != ix {
			if last != emptyLocation {
				setNextLocation(last, this)
			}
			last = this
			if freeChunk == emptyLocation {
				freeChunk = this
			}
		} else {
			slabs[ix].free--
		}
	}

	if slabs[ix].free > 0 {
		panic("ASSERT: Unable to remove all chunks from free-chain")
	}
	slabs[ix] = nil
	allocatedSlabs--
}

// grab a free chunk
func grabChunk() uint32 {
	memoryMutex.Lock()
	defer memoryMutex.Unlock()

	if freeChunk == emptyLocation {
		allocateSlab()
	}

	grabbed := freeChunk
	freeChunk = getNextLocation(grabbed)
	grabbedChunks++

	s, i := getChunkLocation(grabbed)
	if slabs[s].used[i] {
		panic(fmt.Sprintf("ASSERT: Grabbing chunk already in use %x", grabbed))
	}
	slabs[s].used[i] = true
	slabs[s].free--
	slabs[s].touched = time.Now()

	setNextLocation(grabbed, emptyLocation)
	return grabbed
}

func releaseChunk(release uint32) {
	memoryMutex.Lock()
	defer memoryMutex.Unlock()

	s, i := getChunkLocation(release)
	if !slabs[s].used[i] {
		panic(fmt.Sprintf("ASSERT: Releasing chunk not in use %x", release))
	}
	slabs[s].used[i] = false
	slabs[s].free++
	slabs[s].touched = time.Now()

	setNextLocation(release, freeChunk)
	freeChunk = release
	releasedChunks++
}
