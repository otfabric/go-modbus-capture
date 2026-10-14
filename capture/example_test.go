// SPDX-License-Identifier: MIT

package capture_test

import (
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
)

// SplitAddresses turns an FC01–04 sample into one raw value per address,
// which is how a consumer builds per-address timelines. Nothing is decoded:
// registers stay unsigned 16-bit words.
func ExampleSplitAddresses() {
	s := capture.Sample{
		Request:  []byte{0x03, 0x10, 0x00, 0x00, 0x02},       // FC03, address 4096, 2 registers
		Response: []byte{0x03, 0x04, 0x00, 0x01, 0x01, 0xa7}, // 0x0001, 0x01a7
	}
	av, ok := capture.SplitAddresses(&s)
	fmt.Println(ok)
	for i, v := range av.Values {
		fmt.Printf("FC%02d address %d = %d\n", av.Function, int(av.Start)+i, v)
	}
	// Output:
	// true
	// FC03 address 4096 = 1
	// FC03 address 4097 = 423
}

// A Checker enforces the model's rules; every reader and writer uses one.
// Writers drop what a capture does not store, without error.
func ExampleChecker() {
	c, err := capture.NewChecker(capture.Header{TickNS: 1000, WallStart: time.Unix(0, 0), ChangeOnly: true})
	if err != nil {
		log.Fatal(err)
	}
	_ = c.Target(capture.Target{ID: 1})
	_ = c.Stream(capture.Stream{ID: 1, Target: 1})
	req, resp := []byte{0x04, 0, 0, 0, 1}, []byte{0x04, 2, 0, 7}
	for i := range 2 {
		keep, err := c.Admit(capture.Sample{Stream: 1, At: capture.Ticks(i), Request: req, Response: resp})
		fmt.Println(keep, err)
	}
	// A write is never a valid sample: captures hold reads only.
	_, err = c.Admit(capture.Sample{Stream: 1, Request: []byte{0x06, 0, 1, 0, 2}, Response: []byte{0x06, 0, 1, 0, 2}})
	fmt.Println(err)
	// Output:
	// true <nil>
	// false <nil>
	// capture: corrupt: request function code 0x06 is a write; captures hold reads only
}
