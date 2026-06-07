package memory

import (
	"encoding/binary"
	"fmt"
	"math"
)

// float32Bytes is the on-disk width of a single embedding component.
const float32Bytes = 4

// encodeVector serializes a float32 vector to a little-endian byte slice, 4
// bytes per component, for storage as a SQLite BLOB.
func encodeVector(v []float32) []byte {
	buf := make([]byte, len(v)*float32Bytes)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*float32Bytes:], math.Float32bits(f))
	}
	return buf
}

// decodeVector reconstructs a float32 vector from its little-endian BLOB
// encoding. It errors if the byte length is not a whole number of float32s.
func decodeVector(b []byte) ([]float32, error) {
	if len(b)%float32Bytes != 0 {
		return nil, fmt.Errorf("vector blob length %d is not a multiple of %d", len(b), float32Bytes)
	}
	v := make([]float32, len(b)/float32Bytes)
	for i := range v {
		bits := binary.LittleEndian.Uint32(b[i*float32Bytes:])
		v[i] = math.Float32frombits(bits)
	}
	return v, nil
}
