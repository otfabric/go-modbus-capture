// SPDX-License-Identifier: MIT

package mbcap_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/mbcap"
)

// A recorder writes a capture: the header first, then the target and stream
// it polls, then one sample per answered request, and the end on close.
// Sample times are ticks since the capture start, taken from a monotonic
// clock (time.Since of the start time).
func ExampleNewWriter() {
	start := time.Date(2026, 10, 14, 13, 14, 24, 0, time.UTC)
	h := capture.Header{TickNS: capture.DefaultTickNS, WallStart: start, Producer: "my-recorder 1.0"}

	var file bytes.Buffer // an *os.File in practice
	w, err := mbcap.NewWriter(&file, h, nil)
	if err != nil {
		log.Fatal(err)
	}
	_ = w.WriteTarget(capture.Target{ID: 1, Label: "Inverter A", Transport: capture.TransportTCP,
		Host: "192.168.2.201", Port: 502, Unit: 21})
	_ = w.WriteStream(capture.Stream{ID: 1, Target: 1, Role: capture.RolePoll})

	// FC03: read 2 holding registers at 0x1000. The device answered 0x0001, 0x01a7.
	req := []byte{0x03, 0x10, 0x00, 0x00, 0x02}
	resp := []byte{0x03, 0x04, 0x00, 0x01, 0x01, 0xa7}
	for i := range 3 {
		at := h.Ticks(time.Duration(i) * time.Second)
		if err := w.WriteSample(capture.Sample{Stream: 1, At: at, Request: req, Response: resp}); err != nil {
			log.Fatal(err)
		}
	}
	if err := w.Close(capture.End{Known: true, At: h.Ticks(3 * time.Second)}); err != nil {
		log.Fatal(err)
	}
	fmt.Println(file.Len() < 300)
	// Output: true
}

// A consumer reads a capture record by record. Definitions come before the
// samples that use them; a file cut short by a crash is readable up to its
// last complete block.
func ExampleNewReader() {
	r, err := mbcap.NewReader(bytes.NewReader(exampleFile()))
	if err != nil {
		log.Fatal(err)
	}
	h := r.Header()
	for {
		rec, err := r.Next()
		if err == io.EOF {
			break
		}
		if errors.Is(err, capture.ErrTruncated) {
			fmt.Println("truncated: the end is unknown")
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		switch v := rec.(type) {
		case capture.Target:
			fmt.Printf("target %d %q unit %d\n", v.ID, v.Label, v.Unit)
		case capture.Stream:
			fmt.Printf("stream %d of target %d (%s)\n", v.ID, v.Target, v.Role)
		case capture.Sample:
			fmt.Printf("%v  % x -> % x\n", h.Duration(v.At), v.Request, v.Response)
		case capture.End:
			fmt.Printf("end at %v\n", h.Duration(v.At))
		}
	}
	// Output:
	// target 1 "Inverter A" unit 21
	// stream 1 of target 1 (poll)
	// 0s  03 10 00 00 02 -> 03 04 00 01 01 a7
	// 1s  03 10 00 00 02 -> 03 04 00 01 01 a8
	// end at 2s
}

// ReadIndex reads the index from the end of a complete file, without reading
// the data: enough for the span and block layout of a large capture.
func ExampleReadIndex() {
	file := exampleFile()
	idx, err := mbcap.ReadIndex(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("end known:", idx.End.Known, "at tick", idx.End.At)
	for _, b := range idx.Blocks {
		fmt.Println("data block: ticks", b.First, "to", b.Last, "streams", b.Streams)
	}
	// Output:
	// end known: true at tick 2000000
	// data block: ticks 0 to 1000000 streams [1]
}

func exampleFile() []byte {
	h := capture.Header{TickNS: 1000, WallStart: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)}
	var buf bytes.Buffer
	w, _ := mbcap.NewWriter(&buf, h, nil)
	_ = w.WriteTarget(capture.Target{ID: 1, Label: "Inverter A", Transport: capture.TransportTCP, Unit: 21})
	_ = w.WriteStream(capture.Stream{ID: 1, Target: 1, Role: capture.RolePoll})
	req := []byte{0x03, 0x10, 0x00, 0x00, 0x02}
	_ = w.WriteSample(capture.Sample{Stream: 1, At: 0, Request: req, Response: []byte{0x03, 0x04, 0x00, 0x01, 0x01, 0xa7}})
	_ = w.WriteSample(capture.Sample{Stream: 1, At: 1_000_000, Request: req, Response: []byte{0x03, 0x04, 0x00, 0x01, 0x01, 0xa8}})
	_ = w.Close(capture.End{Known: true, At: 2_000_000})
	return buf.Bytes()
}
