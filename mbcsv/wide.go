// SPDX-License-Identifier: MIT

package mbcsv

import (
	"encoding/csv"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"

	"github.com/otfabric/go-modbus-capture/capture"
)

// WideStats says what WriteWide left out.
type WideStats struct {
	// Rows is the number of rows written, column names not counted.
	Rows int
	// Columns is the number of value columns.
	Columns int
	// Skipped counts FC01–04 samples left out: exception responses and
	// responses whose size does not match the request.
	Skipped int
}

type wideKey struct {
	target uint32
	fc     uint8
	addr   uint16
}

// WriteWide writes the wide CSV view of c: one row per sample time of an
// FC01–04 sample, one column per (target, function, address), named
// "<target>/<fc>/<address>" with the function code as two hex digits. Each
// cell holds the latest value sampled at or before the row's time (sample and
// hold), or is empty before the first sample of its address. Values are raw:
// 0 or 1 for coils and discrete inputs, unsigned 16-bit words for registers.
// Samples are taken in time order; samples at the same time share a row.
//
// The view cannot be read back: it drops request PDUs, other functions and
// per-sample identity.
func WriteWide(w io.Writer, c *capture.Capture) (WideStats, error) {
	var st WideStats
	targetOf := make(map[uint32]uint32, len(c.Streams))
	for _, s := range c.Streams {
		targetOf[s.ID] = s.Target
	}
	type update struct {
		at  capture.Ticks
		key wideKey
		av  capture.AddressValues
	}
	var updates []update
	cols := map[wideKey]int{}
	for i := range c.Samples {
		s := &c.Samples[i]
		switch s.Function() {
		case 0x01, 0x02, 0x03, 0x04:
		default:
			continue
		}
		av, ok := capture.SplitAddresses(s)
		if !ok {
			st.Skipped++
			continue
		}
		k := wideKey{target: targetOf[s.Stream], fc: av.Function, addr: av.Start}
		updates = append(updates, update{at: s.At, key: k, av: av})
		for j := range av.Values {
			cols[wideKey{k.target, k.fc, av.Start + uint16(j)}] = 0
		}
	}
	keys := make([]wideKey, 0, len(cols))
	for k := range cols {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b wideKey) int {
		switch {
		case a.target != b.target:
			return cmpUint(uint64(a.target), uint64(b.target))
		case a.fc != b.fc:
			return cmpUint(uint64(a.fc), uint64(b.fc))
		default:
			return cmpUint(uint64(a.addr), uint64(b.addr))
		}
	})
	names := make([]string, 1, 1+len(keys))
	names[0] = "at"
	for i, k := range keys {
		cols[k] = i
		names = append(names, fmt.Sprintf("%d/%02x/%d", k.target, k.fc, k.addr))
	}
	st.Columns = len(keys)

	cw := csv.NewWriter(w)
	if err := cw.Write(names); err != nil {
		return st, err
	}
	sort.SliceStable(updates, func(i, j int) bool { return updates[i].at < updates[j].at })
	cells := make([]string, len(keys))
	row := make([]string, 1+len(keys))
	for i := 0; i < len(updates); {
		at := updates[i].at
		for ; i < len(updates) && updates[i].at == at; i++ {
			u := updates[i]
			base := cols[u.key]
			for j, v := range u.av.Values {
				cells[base+j] = strconv.FormatUint(uint64(v), 10)
			}
		}
		row[0] = strconv.FormatUint(uint64(at), 10)
		copy(row[1:], cells)
		if err := cw.Write(row); err != nil {
			return st, err
		}
		st.Rows++
	}
	cw.Flush()
	return st, cw.Error()
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
