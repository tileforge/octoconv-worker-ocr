package convert

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
)

const DimensionPeekBytes = 64 * 1024

// ErrDimensionsUnknown means the declared width and height could not be read.
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
	format = NormalizeFormat(format)
	switch format {
	case "tiff":
		return readTIFFDimensions(r)
	case "jpg":
		return readJPEGDimensions(r)
	}
	buf := make([]byte, DimensionPeekBytes)
	n, readErr := io.ReadFull(r, buf)
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		return 0, 0, readErr
	}
	buf = buf[:n]
	parser, ok := dimensionParsers[format]
	if !ok {
		return 0, 0, ErrDimensionsUnknown
	}
	w, h, found := parser(buf)
	if !found {
		return 0, 0, ErrDimensionsUnknown
	}
	return w, h, nil
}

// The first IFD offset is in the header and is often past the image data.
// The read stops at the file size, which is capped at MaxFileBytes.
func readTIFFDimensions(r io.Reader) (uint32, uint32, error) {
	ra, ok := r.(io.ReaderAt)
	if !ok {
		return 0, 0, ErrDimensionsUnknown
	}
	size, ok := readerSize(r)
	if !ok || size < 8 {
		return 0, 0, ErrDimensionsUnknown
	}
	var header [8]byte
	if _, err := ra.ReadAt(header[:], 0); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, 0, ErrDimensionsUnknown
		}
		return 0, 0, err
	}
	bo, ifdOffset, ok := tiffHeader(header[:])
	if !ok {
		return 0, 0, ErrDimensionsUnknown
	}
	dir, ok, err := readTIFFDirectory(ra, size, bo, ifdOffset)
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		return 0, 0, ErrDimensionsUnknown
	}
	w, h, found := tiffIFDDimensions(bo, dir)
	if !found {
		return 0, 0, ErrDimensionsUnknown
	}
	return w, h, nil
}

func readerSize(r io.Reader) (int64, bool) {
	type stater interface {
		Stat() (fs.FileInfo, error)
	}
	if s, ok := r.(stater); ok {
		info, err := s.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return 0, false
		}
		return boundedSize(info.Size())
	}
	type sizer interface {
		Size() int64
	}
	if s, ok := r.(sizer); ok {
		return boundedSize(s.Size())
	}
	return 0, false
}

func boundedSize(size int64) (int64, bool) {
	if size < 0 || size > MaxFileBytes {
		return 0, false
	}
	return size, true
}

func readTIFFDirectory(ra io.ReaderAt, size int64, bo binary.ByteOrder, ifdOffset uint64) ([]byte, bool, error) {
	if ifdOffset > uint64(size) || uint64(size)-ifdOffset < 2 {
		return nil, false, nil
	}
	var countBuf [2]byte
	if _, err := ra.ReadAt(countBuf[:], int64(ifdOffset)); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, false, nil
		}
		return nil, false, err
	}
	count := uint64(bo.Uint16(countBuf[:]))
	dirLen := 2 + count*12
	if dirLen > uint64(size)-ifdOffset {
		return nil, false, nil
	}
	dir := make([]byte, dirLen)
	if _, err := ra.ReadAt(dir, int64(ifdOffset)); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return dir, true, nil
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

func readAt(ra io.ReaderAt, buf []byte, off int64) error {
	n, err := ra.ReadAt(buf, off)
	if n == len(buf) {
		return nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return io.ErrUnexpectedEOF
}

// A JPEG SOF marker can follow a comment larger than the 64 KiB prefix.
// The walk uses each segment length and stops at the scan or the file size.
func readJPEGDimensions(r io.Reader) (uint32, uint32, error) {
	ra, ok := r.(io.ReaderAt)
	if !ok {
		return 0, 0, ErrDimensionsUnknown
	}
	size, ok := readerSize(r)
	if !ok || size < 4 {
		return 0, 0, ErrDimensionsUnknown
	}
	var soi [2]byte
	if err := readAt(ra, soi[:], 0); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, 0, ErrDimensionsUnknown
		}
		return 0, 0, err
	}
	if soi[0] != 0xFF || soi[1] != 0xD8 {
		return 0, 0, ErrDimensionsUnknown
	}
	off := int64(2)
	for off+1 < size {
		var mark [4]byte
		if err := readAt(ra, mark[:2], off); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return 0, 0, ErrDimensionsUnknown
			}
			return 0, 0, err
		}
		if mark[0] != 0xFF {
			return 0, 0, ErrDimensionsUnknown
		}
		marker := mark[1]
		if marker == 0xFF {
			off++
			continue
		}
		if marker == 0x01 || marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7) {
			off += 2
			continue
		}
		if marker == 0xD9 || marker == 0xDA {
			return 0, 0, ErrDimensionsUnknown
		}
		if off+4 > size {
			return 0, 0, ErrDimensionsUnknown
		}
		if err := readAt(ra, mark[:4], off); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return 0, 0, ErrDimensionsUnknown
			}
			return 0, 0, err
		}
		segLen := int64(mark[2])<<8 | int64(mark[3])
		if segLen < 2 {
			return 0, 0, ErrDimensionsUnknown
		}
		isSOF := marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC
		if isSOF {
			if off+9 > size {
				return 0, 0, ErrDimensionsUnknown
			}
			var sof [9]byte
			if err := readAt(ra, sof[:], off); err != nil {
				if errors.Is(err, io.ErrUnexpectedEOF) {
					return 0, 0, ErrDimensionsUnknown
				}
				return 0, 0, err
			}
			height := uint32(sof[5])<<8 | uint32(sof[6])
			width := uint32(sof[7])<<8 | uint32(sof[8])
			return width, height, nil
		}
		next := off + 2 + segLen
		if next < off || next > size {
			return 0, 0, ErrDimensionsUnknown
		}
		off = next
	}
	return 0, 0, ErrDimensionsUnknown
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

func tiffHeader(b []byte) (binary.ByteOrder, uint64, bool) {
	if len(b) < 8 {
		return nil, 0, false
	}
	var bo binary.ByteOrder
	switch {
	case bytes.Equal(b[0:2], []byte{0x49, 0x49}):
		bo = binary.LittleEndian
	case bytes.Equal(b[0:2], []byte{0x4D, 0x4D}):
		bo = binary.BigEndian
	default:
		return nil, 0, false
	}
	if bo.Uint16(b[2:4]) != 42 {
		return nil, 0, false
	}
	return bo, uint64(bo.Uint32(b[4:8])), true
}

func tiffDimensions(b []byte) (uint32, uint32, bool) {
	bo, ifdOffset, ok := tiffHeader(b)
	if !ok || ifdOffset > uint64(len(b)) || uint64(len(b))-ifdOffset < 2 {
		return 0, 0, false
	}
	count := uint64(bo.Uint16(b[ifdOffset : ifdOffset+2]))
	dirLen := 2 + count*12
	if dirLen > uint64(len(b))-ifdOffset {
		return 0, 0, false
	}
	return tiffIFDDimensions(bo, b[ifdOffset:ifdOffset+dirLen])
}

func tiffIFDDimensions(bo binary.ByteOrder, dir []byte) (uint32, uint32, bool) {
	if len(dir) < 2 {
		return 0, 0, false
	}
	count := uint32(bo.Uint16(dir[0:2]))
	var width, height uint32
	var foundW, foundH bool
	for i := uint32(0); i < count; i++ {
		off := 2 + i*12
		if uint64(off)+12 > uint64(len(dir)) {
			return 0, 0, false
		}
		tag := bo.Uint16(dir[off : off+2])
		if tag != 256 && tag != 257 {
			continue
		}
		typ := bo.Uint16(dir[off+2 : off+4])
		var val uint32
		switch typ {
		case 3:
			val = uint32(bo.Uint16(dir[off+8 : off+10]))
		case 4:
			val = bo.Uint32(dir[off+8 : off+12])
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
