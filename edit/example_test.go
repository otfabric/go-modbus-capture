// SPDX-License-Identifier: MIT

package edit_test

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/edit"
	"github.com/otfabric/go-modbus-capture/jsonl"
)

const capJSONL = `{"t":"header","version":"0.1","tick_ns":1000,"wall_start":"2026-10-14T13:14:24Z"}
{"t":"target","id":1,"transport":"tcp","unit":21}
{"t":"stream","id":1,"target":1,"role":"poll"}
{"t":"s","s":1,"at":0,"req":"0310000002","resp":"0304000101a7"}
{"t":"s","s":1,"at":1000000,"req":"0400000001","resp":"04020005"}
{"t":"s","s":1,"at":2000000,"req":"0310000002","resp":"0304000101a8"}
{"t":"end","at":3000000}
`

// Filter keeps part of a capture and writes it as a new one, recording the
// operation in the provenance tag. Here: only FC03, as JSON Lines on stdout.
func ExampleFilter() {
	src, err := jsonl.NewReader(strings.NewReader(capJSONL))
	if err != nil {
		log.Fatal(err)
	}
	sel := edit.Selection{Functions: []uint8{0x03}}
	open := func(h capture.Header) (capture.Writer, error) { return jsonl.NewWriter(os.Stdout, h) }
	if _, err := edit.Filter(src, sel, "example filter --fc 3", open); err != nil {
		log.Fatal(err)
	}
	// Output:
	// {"t":"header","version":"0.1","tick_ns":1000,"wall_start":"2026-10-14T13:14:24Z","tags":{"provenance":"example filter --fc 3"}}
	// {"t":"target","id":1,"transport":"tcp","unit":21}
	// {"t":"stream","id":1,"target":1,"role":"poll"}
	// {"t":"s","s":1,"at":0,"req":"0310000002","resp":"0304000101a7"}
	// {"t":"s","s":1,"at":2000000,"req":"0310000002","resp":"0304000101a8"}
	// {"t":"end","at":3000000}
}

// Split writes one capture per part; here one per function code.
func ExampleSplit() {
	src, err := jsonl.NewReader(strings.NewReader(capJSONL))
	if err != nil {
		log.Fatal(err)
	}
	counts, err := edit.Split(src, edit.ByFunction, "", func(key string, h capture.Header) (capture.Writer, error) {
		return jsonl.NewWriter(discard{}, h) // a file per key in practice
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(counts)
	// Output: map[fc-03:2 fc-04:1]
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
