package saga

import (
	"encoding/binary"
	"hash/crc32"
)

// FrameForTest builds a checksummed log frame with an arbitrary declared
// payload length n and payload bytes for codec boundary tests.
func FrameForTest(n int, payload []byte) []byte {
	var h [headerSize]byte
	h[0] = frameMagic
	binary.BigEndian.PutUint32(h[1:5], uint32(n))
	binary.BigEndian.PutUint32(h[5:9], crc32.Checksum(h[:5], castagnoli))
	binary.BigEndian.PutUint32(h[9:13], crc32.Checksum(payload, castagnoli))
	return append(h[:], payload...)
}

// PayloadForTest slices the raw payload bytes out of an encoded log frame.
func PayloadForTest(frame []byte) []byte {
	return frame[headerSize:]
}

// PlantSkippedCompensation configures cfg so the engine logs step's
// compensation as done without calling Step.Compensate, used by canary tests.
func PlantSkippedCompensation(cfg *Config, step int) {
	cfg.skipComp = step + 1
}
