package foreigndb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

// file is one opened database file: the base image plus the pages a
// not-yet-checkpointed write-ahead log supersedes. Pages are read on
// demand from the base file; only log-superseded pages are held in
// memory, and a log is small next to the database it journals.
type file struct {
	base     *os.File
	pageSize int
	reserved int // reserved bytes at the end of every page
	encoding uint32
	pages    map[uint32][]byte // page number -> full page image (from the log)
	pageCnt  uint32
}

// fileMagic is the 16-byte signature at offset 0 of every file in this
// format (the format's own name for itself, NUL-terminated).
var fileMagic = []byte{
	0x53, 0x51, 0x4c, 0x69, 0x74, 0x65, 0x20, 0x66,
	0x6f, 0x72, 0x6d, 0x61, 0x74, 0x20, 0x33, 0x00,
}

const (
	headerSize  = 100
	walHdrSize  = 32
	frameHdrLen = 24
	walMagicLE  = 0x377f0682 // frame checksums sum words little-endian
	walMagicBE  = 0x377f0683 // frame checksums sum words big-endian
)

// openFile opens path read-only and folds in its write-ahead log when
// one is present.
func openFile(path string) (*file, error) {
	base, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	f := &file{base: base, pages: make(map[uint32][]byte)}

	head := make([]byte, headerSize)
	if _, err := base.ReadAt(head, 0); err != nil {
		_ = base.Close()
		return nil, fmt.Errorf("foreigndb: read header: %w", err)
	}
	for i, b := range fileMagic {
		if head[i] != b {
			_ = base.Close()
			return nil, errors.New("foreigndb: not a database file of the expected format")
		}
	}
	f.pageSize = int(binary.BigEndian.Uint16(head[16:18]))
	if f.pageSize == 1 {
		f.pageSize = 65536
	}
	if f.pageSize < 512 || f.pageSize&(f.pageSize-1) != 0 {
		_ = base.Close()
		return nil, fmt.Errorf("foreigndb: implausible page size %d", f.pageSize)
	}
	f.reserved = int(head[20])
	f.encoding = binary.BigEndian.Uint32(head[56:60])
	f.pageCnt = binary.BigEndian.Uint32(head[28:32])
	if f.pageCnt == 0 {
		if info, err := base.Stat(); err == nil {
			f.pageCnt = uint32(info.Size() / int64(f.pageSize))
		}
	}

	if err := f.foldWAL(path + "-wal"); err != nil {
		_ = base.Close()
		return nil, err
	}
	return f, nil
}

func (f *file) Close() error { return f.base.Close() }

func (f *file) usable() int { return f.pageSize - f.reserved }

// page returns the current image of a 1-based page: the log's copy when
// the log superseded it, otherwise the base file's.
func (f *file) page(n uint32) ([]byte, error) {
	if n == 0 {
		return nil, errors.New("foreigndb: page 0 does not exist")
	}
	if img, ok := f.pages[n]; ok {
		return img, nil
	}
	buf := make([]byte, f.pageSize)
	if _, err := f.base.ReadAt(buf, int64(n-1)*int64(f.pageSize)); err != nil {
		return nil, fmt.Errorf("foreigndb: read page %d: %w", n, err)
	}
	return buf, nil
}

// foldWAL applies the committed frames of the write-ahead log at
// walPath. A missing log is the common case, not an error. Frames are
// applied in order; only frames up to the last commit frame whose
// salts match the log header and whose running checksum verifies are
// visible — the same visibility rule the format's own readers apply,
// so a torn tail frame can never surface as data.
func (f *file) foldWAL(walPath string) error {
	wal, err := os.Open(walPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("foreigndb: open write-ahead log: %w", err)
	}
	defer func() { _ = wal.Close() }()

	hdr := make([]byte, walHdrSize)
	if _, err := wal.ReadAt(hdr, 0); err != nil {
		return nil // an empty or truncated log journals nothing
	}
	magic := binary.BigEndian.Uint32(hdr[0:4])
	if magic != walMagicLE && magic != walMagicBE {
		return nil
	}
	walPageSize := int(binary.BigEndian.Uint32(hdr[8:12]))
	if walPageSize != f.pageSize {
		return nil
	}
	salt1 := binary.BigEndian.Uint32(hdr[16:20])
	salt2 := binary.BigEndian.Uint32(hdr[20:24])

	// The running checksum starts over the log header's first 24 bytes.
	var s1, s2 uint32
	s1, s2 = walChecksum(magic, hdr[:24], 0, 0)
	if s1 != binary.BigEndian.Uint32(hdr[24:28]) || s2 != binary.BigEndian.Uint32(hdr[28:32]) {
		return nil
	}

	frameSize := int64(frameHdrLen + f.pageSize)
	pending := make(map[uint32][]byte)
	var dbPages uint32 = f.pageCnt
	offset := int64(walHdrSize)
	for {
		fh := make([]byte, frameHdrLen)
		if _, err := wal.ReadAt(fh, offset); err != nil {
			break
		}
		pageNo := binary.BigEndian.Uint32(fh[0:4])
		commitSize := binary.BigEndian.Uint32(fh[4:8])
		if binary.BigEndian.Uint32(fh[8:12]) != salt1 || binary.BigEndian.Uint32(fh[12:16]) != salt2 {
			break
		}
		img := make([]byte, f.pageSize)
		if _, err := wal.ReadAt(img, offset+frameHdrLen); err != nil {
			break
		}
		s1, s2 = walChecksum(magic, fh[:8], s1, s2)
		s1, s2 = walChecksum(magic, img, s1, s2)
		if s1 != binary.BigEndian.Uint32(fh[16:20]) || s2 != binary.BigEndian.Uint32(fh[20:24]) {
			break // torn or foreign frame: everything past here is invisible
		}
		pending[pageNo] = img
		offset += frameSize
		if commitSize != 0 {
			for n, p := range pending {
				f.pages[n] = p
			}
			pending = make(map[uint32][]byte)
			dbPages = commitSize
		}
	}
	f.pageCnt = dbPages
	return nil
}

// walChecksum is the format's frame checksum: word-wise, wrapping,
// with the word order the log's magic selects.
func walChecksum(magic uint32, data []byte, s1, s2 uint32) (uint32, uint32) {
	bigEndianWords := magic == walMagicBE
	for i := 0; i+8 <= len(data); i += 8 {
		var x, y uint32
		if bigEndianWords {
			x = binary.BigEndian.Uint32(data[i : i+4])
			y = binary.BigEndian.Uint32(data[i+4 : i+8])
		} else {
			x = binary.LittleEndian.Uint32(data[i : i+4])
			y = binary.LittleEndian.Uint32(data[i+4 : i+8])
		}
		s1 += x + s2
		s2 += y + s1
	}
	return s1, s2
}
