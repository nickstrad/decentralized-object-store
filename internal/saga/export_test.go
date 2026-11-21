package saga

import (
	"encoding/binary"
	"hash/crc32"
)

func FrameForTest(n int, payload []byte) []byte {
	var h [headerSize]byte
	h[0] = frameMagic
	binary.BigEndian.PutUint32(h[1:5], uint32(n))
	binary.BigEndian.PutUint32(h[5:9], crc32.Checksum(h[:5], castagnoli))
	binary.BigEndian.PutUint32(h[9:13], crc32.Checksum(payload, castagnoli))
	return append(h[:], payload...)
}

func PayloadForTest(frame []byte) []byte {
	return frame[headerSize:]
}

func PlantSkippedCompensation(cfg *Config, step int) {
	cfg.skipComp = step + 1
}
