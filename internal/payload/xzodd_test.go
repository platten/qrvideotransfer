package payload

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
)

// TestXZTruncatedAndOdd: every prefix of a stream is refused as a format error, and so are a few
// malformations the other tests do not reach.
func TestXZTruncatedAndOdd(t *testing.T) {
	good := xzOf(t, append([]byte("hello "), pseudoRandom(100)...), 0)
	for n := range len(good) {
		if _, err := decodeXZ(good[:n]); !isFormat(err) {
			t.Fatalf("prefix of %d bytes: %v", n, err)
		}
	}
	checkType := append(bytes.Clone(xzNone[:7]), 0x02)
	checkType = binary.LittleEndian.AppendUint32(checkType, crc32.ChecksumIEEE(checkType[6:8]))
	footer := bytes.Clone(good)
	footer[len(footer)-12] ^= 1
	one := func(chunks []byte, out uint64) []byte {
		b, r := rawBlock(chunks, out)
		return xzStream(xzNone, b, [][2]uint64{r})
	}
	for name, data := range map[string][]byte{
		"check type 0x02":          append(checkType, good[12:]...),
		"footer checksum":          footer,
		"chunk control 0x03":       one([]byte{0x01, 0, 0, 'x', 0x03}, 1),
		"compressed size 0":        replaceBlockHeader(good, blockHeader(0x40, 0x00, 0x21, 0x01, 22)),
		"index integer not short":  indexNonShortest(),
		"LZMA without properties":  one([]byte{0x01, 0, 0, 'x', 0x80, 0, 0, 0, 0}, 2),
		"LZMA2 properties lc+lp 5": one([]byte{0xe0, 0, 0, 0, 0, 0x05, 0}, 1),
	} {
		if _, err := decodeXZ(data); !isFormat(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n, err := newXZReader(bytes.NewReader(good)).Read(nil); n != 0 || err != nil {
		t.Fatalf("empty read: %d, %v", n, err)
	}
}

// indexNonShortest returns a valid one-block stream except that its index writes the record count 1
// in two bytes (0x81 0x00), which is not the shortest encoding.
func indexNonShortest() []byte {
	b, rec := rawBlock([]byte{0x01, 0, 0, 'x'}, 1)
	index := []byte{0, 0x81, 0x00}
	index = binary.AppendUvarint(binary.AppendUvarint(index, rec[0]), rec[1])
	for len(index)%4 != 0 {
		index = append(index, 0)
	}
	index = binary.LittleEndian.AppendUint32(index, crc32.ChecksumIEEE(index))
	s := append(append(bytes.Clone(xzNone), b...), index...)
	f := binary.LittleEndian.AppendUint32(nil, uint32(len(index)/4-1))
	f = append(f, xzNone[6], xzNone[7])
	s = binary.LittleEndian.AppendUint32(s, crc32.ChecksumIEEE(f))
	return append(append(s, f...), 'Y', 'Z')
}
