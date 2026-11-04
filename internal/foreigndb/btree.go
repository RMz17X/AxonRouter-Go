package foreigndb

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"unicode/utf16"
)

// readVarint decodes the format's big-endian variable-length integer:
// up to eight 7-bit groups, with a ninth byte contributing all 8 bits.
func readVarint(b []byte, off int) (int64, int, bool) {
	var v int64
	for i := 0; i < 8; i++ {
		if off+i >= len(b) {
			return 0, 0, false
		}
		c := b[off+i]
		v = (v << 7) | int64(c&0x7f)
		if c&0x80 == 0 {
			return v, i + 1, true
		}
	}
	if off+8 >= len(b) {
		return 0, 0, false
	}
	v = (v << 8) | int64(b[off+8])
	return v, 9, true
}

// decodeRecord splits one record payload into its column values. The
// header is a length varint followed by one serial-type varint per
// column; the body follows in the same order.
func decodeRecord(payload []byte, encoding uint32) ([]any, error) {
	hdrLen, n, ok := readVarint(payload, 0)
	if !ok || hdrLen < 1 || int(hdrLen) > len(payload) {
		return nil, fmt.Errorf("foreigndb: corrupt record header")
	}
	var types []int64
	off := n
	for off < int(hdrLen) {
		t, m, ok := readVarint(payload, off)
		if !ok {
			return nil, fmt.Errorf("foreigndb: corrupt record serial types")
		}
		types = append(types, t)
		off += m
	}
	body := int(hdrLen)
	vals := make([]any, len(types))
	for i, t := range types {
		size := serialSize(t)
		if body+size > len(payload) {
			return nil, fmt.Errorf("foreigndb: record overruns its payload")
		}
		raw := payload[body : body+size]
		body += size
		v, err := serialValue(t, raw, encoding)
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	return vals, nil
}

func serialSize(t int64) int {
	switch {
	case t == 0 || t == 8 || t == 9:
		return 0
	case t == 1:
		return 1
	case t == 2:
		return 2
	case t == 3:
		return 3
	case t == 4:
		return 4
	case t == 5:
		return 6
	case t == 6 || t == 7:
		return 8
	case t >= 12:
		return int((t - 12) / 2)
	}
	return 0
}

func serialValue(t int64, raw []byte, encoding uint32) (any, error) {
	switch {
	case t == 0:
		return nil, nil
	case t >= 1 && t <= 6:
		var v int64
		for _, c := range raw {
			v = (v << 8) | int64(c)
		}
		// Sign-extend from the value's own width.
		shift := uint(64 - 8*len(raw))
		return (v << shift) >> shift, nil
	case t == 7:
		return math.Float64frombits(binary.BigEndian.Uint64(raw)), nil
	case t == 8:
		return int64(0), nil
	case t == 9:
		return int64(1), nil
	case t >= 13 && t%2 == 1:
		return decodeText(raw, encoding), nil
	case t >= 12:
		out := make([]byte, len(raw))
		copy(out, raw)
		return out, nil
	}
	return nil, fmt.Errorf("foreigndb: reserved serial type %d", t)
}

// decodeText renders a stored text value as a Go string under the
// file's declared encoding (UTF-8 unless the header says UTF-16).
func decodeText(raw []byte, encoding uint32) string {
	switch encoding {
	case 2: // UTF-16 little-endian
		return decodeUTF16(raw, binary.LittleEndian)
	case 3: // UTF-16 big-endian
		return decodeUTF16(raw, binary.BigEndian)
	}
	return string(raw)
}

func decodeUTF16(raw []byte, order binary.ByteOrder) string {
	if len(raw)%2 != 0 {
		return string(raw)
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = order.Uint16(raw[2*i : 2*i+2])
	}
	return string(utf16.Decode(units))
}

// tableInfo is one table's place in the file: where its b-tree starts
// and the column names in storage order.
type tableInfo struct {
	rootPage uint32
	columns  []string
}

// findTable looks a table up in the schema table, which itself lives
// at page 1 with records of (type, name, tbl_name, rootpage, sql).
func (f *file) findTable(name string) (*tableInfo, error) {
	rows, err := f.walkTable(1, 0)
	if err != nil {
		return nil, err
	}
	for _, rec := range rows {
		if len(rec) < 5 {
			continue
		}
		typ, _ := rec[0].(string)
		tblName, _ := rec[1].(string)
		if typ != "table" || !strings.EqualFold(tblName, name) {
			continue
		}
		root, _ := rec[3].(int64)
		if root == 0 {
			return nil, fmt.Errorf("foreigndb: table %q has no rowid b-tree", name)
		}
		sqlText, _ := rec[4].(string)
		if strings.Contains(strings.ToUpper(sqlText), "WITHOUT ROWID") {
			return nil, fmt.Errorf("foreigndb: table %q uses a key-ordered b-tree this reader does not walk", name)
		}
		cols, err := parseColumnList(sqlText)
		if err != nil {
			return nil, err
		}
		return &tableInfo{rootPage: uint32(root), columns: cols}, nil
	}
	return nil, fmt.Errorf("foreigndb: no such table: %s", name)
}

// parseColumnList extracts the column names, in order, from a
// CREATE TABLE statement's parenthesised body. Constraint clauses
// (PRIMARY KEY, FOREIGN KEY, ...) are not columns and are skipped.
func parseColumnList(createSQL string) ([]string, error) {
	open := strings.Index(createSQL, "(")
	closeIdx := strings.LastIndex(createSQL, ")")
	if open < 0 || closeIdx <= open {
		return nil, fmt.Errorf("foreigndb: cannot parse the table definition")
	}
	body := createSQL[open+1 : closeIdx]
	var parts []string
	depth := 0
	start := 0
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, body[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, body[start:])

	var cols []string
	for _, part := range parts {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) == 0 {
			continue
		}
		name := strings.Trim(fields[0], "\"`[]")
		switch strings.ToUpper(name) {
		case "PRIMARY", "FOREIGN", "UNIQUE", "CHECK", "CONSTRAINT":
			continue
		}
		cols = append(cols, name)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("foreigndb: the table definition names no columns")
	}
	return cols, nil
}

// walkTable returns every record of the table b-tree rooted at
// rootPage, in rowid order. depth bounds the recursion so a corrupt
// interior page cannot loop the walk.
func (f *file) walkTable(rootPage uint32, depth int) ([][]any, error) {
	if depth > 100 {
		return nil, fmt.Errorf("foreigndb: b-tree deeper than any real file")
	}
	page, err := f.page(rootPage)
	if err != nil {
		return nil, err
	}
	start := 0
	if rootPage == 1 {
		start = headerSize // page 1 begins after the file header
	}
	if start+8 > len(page) {
		return nil, fmt.Errorf("foreigndb: page %d too small for a b-tree header", rootPage)
	}
	kind := page[start]
	cellCount := int(binary.BigEndian.Uint16(page[start+3 : start+5]))
	hdrLen := 8
	if kind == 0x05 {
		hdrLen = 12
	} else if kind != 0x0d {
		return nil, fmt.Errorf("foreigndb: page %d is not a table b-tree page (type %#x)", rootPage, kind)
	}
	ptrBase := start + hdrLen
	if ptrBase+2*cellCount > len(page) {
		return nil, fmt.Errorf("foreigndb: page %d cell pointer array overruns the page", rootPage)
	}

	var out [][]any
	if kind == 0x05 {
		for i := 0; i < cellCount; i++ {
			// Cell pointers are absolute from the start of the page.
			cellOff := int(binary.BigEndian.Uint16(page[ptrBase+2*i : ptrBase+2*i+2]))
			if cellOff+4 > len(page) {
				return nil, fmt.Errorf("foreigndb: interior cell overruns page %d", rootPage)
			}
			child := binary.BigEndian.Uint32(page[cellOff : cellOff+4])
			sub, err := f.walkTable(child, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
		}
		rightmost := binary.BigEndian.Uint32(page[start+8 : start+12])
		sub, err := f.walkTable(rightmost, depth+1)
		if err != nil {
			return nil, err
		}
		return append(out, sub...), nil
	}

	for i := 0; i < cellCount; i++ {
		cellOff := int(binary.BigEndian.Uint16(page[ptrBase+2*i : ptrBase+2*i+2]))
		payload, err := f.readCellPayload(page, cellOff)
		if err != nil {
			return nil, err
		}
		rec, err := decodeRecord(payload, f.encoding)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// readCellPayload reads one leaf cell at offset off within page,
// following the overflow chain when the payload does not fit locally.
func (f *file) readCellPayload(page []byte, off int) ([]byte, error) {
	payloadLen, n, ok := readVarint(page, off)
	if !ok {
		return nil, fmt.Errorf("foreigndb: corrupt cell payload length")
	}
	off += n
	if _, m, ok := readVarint(page, off); ok { // the rowid; the value is not needed
		off += m
	} else {
		return nil, fmt.Errorf("foreigndb: corrupt cell rowid")
	}
	usable := int64(f.usable())
	total := payloadLen
	local := total
	if total > usable-35 {
		m := ((usable - 12) * 32 / 255) - 23
		k := m + ((total - m) % (usable - 4))
		local = k
		if k > usable-35 {
			local = m
		}
	}
	if off+int(local) > len(page) {
		return nil, fmt.Errorf("foreigndb: cell payload overruns its page")
	}
	payload := make([]byte, 0, total)
	payload = append(payload, page[off:off+int(local)]...)
	remaining := total - local
	if remaining <= 0 {
		return payload, nil
	}
	off += int(local)
	if off+4 > len(page) {
		return nil, fmt.Errorf("foreigndb: cell is missing its overflow pointer")
	}
	next := binary.BigEndian.Uint32(page[off : off+4])
	for remaining > 0 {
		if next == 0 {
			return nil, fmt.Errorf("foreigndb: overflow chain ends early")
		}
		opage, err := f.page(next)
		if err != nil {
			return nil, err
		}
		if len(opage) < 4 {
			return nil, fmt.Errorf("foreigndb: overflow page too small")
		}
		next = binary.BigEndian.Uint32(opage[0:4])
		take := usable - 4
		if take > remaining {
			take = remaining
		}
		if 4+int(take) > len(opage) {
			return nil, fmt.Errorf("foreigndb: overflow page overruns")
		}
		payload = append(payload, opage[4:4+int(take)]...)
		remaining -= take
	}
	return payload, nil
}
