package convert

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

const DimensionPeekBytes = 64 * 1024

// ErrDimensionsUnknown means the declared width and height were not in the peeked prefix.
var ErrDimensionsUnknown = errors.New("cannot determine declared image dimensions")

type dimensionParser func(buf []byte) (width, height uint32, ok bool)

var dimensionParsers = map[string]dimensionParser{
	"png":  pngDimensions,
	"jpg":  jpegDimensions,
	"webp": webpDimensions,
	"tiff": tiffDimensions,
}

func HasDimensionLimit(format string) bool {
	_, ok := dimensionParsers[NormalizeFormat(format)]
	return ok
}

func readDeclaredDimensions(format string, r io.Reader) (uint32, uint32, error) {
	buf := make([]byte, DimensionPeekBytes)
	n, readErr := io.ReadFull(r, buf)
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		return 0, 0, readErr
	}
	buf = buf[:n]
	parser, ok := dimensionParsers[NormalizeFormat(format)]
	if !ok {
		return 0, 0, ErrDimensionsUnknown
	}
	w, h, found := parser(buf)
	if !found {
		return 0, 0, ErrDimensionsUnknown
	}
	return w, h, nil
}

func pngDimensions(b []byte) (uint32, uint32, bool) {
	if len(b) < 24 || !bytes.Equal(b[12:16], []byte("IHDR")) {
		return 0, 0, false
	}
	return binary.BigEndian.Uint32(b[16:20]), binary.BigEndian.Uint32(b[20:24]), true
}

func webpDimensions(b []byte) (uint32, uint32, bool) {
	if len(b) < 20 || !bytes.Equal(b[0:4], []byte("RIFF")) || !bytes.Equal(b[8:12], []byte("WEBP")) {
		return 0, 0, false
	}
	switch string(b[12:16]) {
	case "VP8X":
		if len(b) < 30 {
			return 0, 0, false
		}
		width := uint32(b[24]) | uint32(b[25])<<8 | uint32(b[26])<<16
		height := uint32(b[27]) | uint32(b[28])<<8 | uint32(b[29])<<16
		return width + 1, height + 1, true
	case "VP8 ":
		if len(b) < 30 || b[23] != 0x9D || b[24] != 0x01 || b[25] != 0x2A {
			return 0, 0, false
		}
		w16 := uint32(b[26]) | uint32(b[27])<<8
		h16 := uint32(b[28]) | uint32(b[29])<<8
		return w16 & 0x3FFF, h16 & 0x3FFF, true
	case "VP8L":
		if len(b) < 25 || b[20] != 0x2F {
			return 0, 0, false
		}
		bits := uint32(b[21]) | uint32(b[22])<<8 | uint32(b[23])<<16 | uint32(b[24])<<24
		return (bits & 0x3FFF) + 1, ((bits >> 14) & 0x3FFF) + 1, true
	default:
		return 0, 0, false
	}
}

func jpegDimensions(b []byte) (uint32, uint32, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 0, 0, false
	}
	i := 2
	for i+1 < len(b) {
		if b[i] != 0xFF {
			i++
			continue
		}
		marker := b[i+1]
		if marker == 0xFF {
			i++
			continue
		}
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		if marker == 0xD8 {
			i += 2
			continue
		}
		if marker == 0xD9 || marker == 0xDA {
			return 0, 0, false
		}
		if i+4 > len(b) {
			return 0, 0, false
		}
		segLen := int(b[i+2])<<8 | int(b[i+3])
		isSOF := marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC
		if isSOF {
			if i+9 > len(b) {
				return 0, 0, false
			}
			height := uint32(b[i+5])<<8 | uint32(b[i+6])
			width := uint32(b[i+7])<<8 | uint32(b[i+8])
			return width, height, true
		}
		if segLen < 2 || i+2+segLen > len(b) {
			return 0, 0, false
		}
		i += 2 + segLen
	}
	return 0, 0, false
}

func tiffDimensions(b []byte) (uint32, uint32, bool) {
	if len(b) < 8 {
		return 0, 0, false
	}
	var bo binary.ByteOrder
	switch {
	case bytes.Equal(b[0:2], []byte{0x49, 0x49}):
		bo = binary.LittleEndian
	case bytes.Equal(b[0:2], []byte{0x4D, 0x4D}):
		bo = binary.BigEndian
	default:
		return 0, 0, false
	}
	ifdOffset := bo.Uint32(b[4:8])
	if uint64(ifdOffset)+2 > uint64(len(b)) {
		return 0, 0, false
	}
	count := bo.Uint16(b[ifdOffset : ifdOffset+2])
	entriesStart := ifdOffset + 2
	var width, height uint32
	var foundW, foundH bool
	for i := uint32(0); i < uint32(count); i++ {
		off := entriesStart + i*12
		if uint64(off)+12 > uint64(len(b)) {
			return 0, 0, false
		}
		tag := bo.Uint16(b[off : off+2])
		if tag != 256 && tag != 257 {
			continue
		}
		typ := bo.Uint16(b[off+2 : off+4])
		var val uint32
		switch typ {
		case 3:
			val = uint32(bo.Uint16(b[off+8 : off+10]))
		case 4:
			val = bo.Uint32(b[off+8 : off+12])
		default:
			continue
		}
		if tag == 256 {
			width, foundW = val, true
		} else {
			height, foundH = val, true
		}
		if foundW && foundH {
			return width, height, true
		}
	}
	return 0, 0, false
}
