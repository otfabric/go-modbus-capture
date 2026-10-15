// SPDX-License-Identifier: MIT

package jsonl

import (
	"errors"
	"strings"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
)

const head = `{"t":"header","version":"0.1","tick_ns":1000,"wall_start":"2026-10-14T13:14:24.701149Z"}` + "\n"
const defs = `{"t":"target","id":1,"transport":"tcp","unit":1}` + "\n" + `{"t":"stream","id":1,"target":1}` + "\n"

func read(s string) (*capture.Capture, error) {
	r, err := NewReader(strings.NewReader(s))
	if err != nil {
		return nil, err
	}
	return capture.ReadAll(r)
}

func TestReaderRules(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		samples int
		err     error
	}{
		{"minimal", head, 0, nil},
		{"unknown line types and fields are ignored", head + `{"t":"note","id":"x"}` + "\n" + defs +
			`{"t":"s","s":1,"at":5,"req":"0300000001","resp":"03020007","note":"hi"}` + "\n", 1, nil},
		{"blank lines are ignored", head + "\n  \n" + defs, 0, nil},
		{"upper-case hex is read", head + defs + `{"t":"s","s":1,"at":5,"req":"0300000001","resp":"030200AB"}` + "\n", 1, nil},
		{"a second header", head + head, 0, capture.ErrCorrupt},
		{"a vendor function code", head + defs + `{"t":"s","s":1,"at":5,"req":"410102","resp":"410a0b"}` + "\n", 0, capture.ErrCorrupt},
		{"a state-changing FC08 sub-function", head + defs + `{"t":"s","s":1,"at":5,"req":"08000a0000","resp":"08000a0000"}` + "\n", 0, capture.ErrCorrupt},
		{"first line not a header", defs, 0, capture.ErrCorrupt},
		{"unknown major version", strings.Replace(head, `"0.1"`, `"1.0"`, 1), 0, capture.ErrCorrupt},
		{"a later minor version", strings.Replace(head, `"0.1"`, `"0.9"`, 1), 0, nil},
		{"missing tick", `{"t":"header","version":"0.1","wall_start":"2026-10-14T13:14:24Z"}` + "\n", 0, capture.ErrCorrupt},
		{"sample without resp", head + defs + `{"t":"s","s":1,"at":5,"req":"03"}` + "\n", 0, capture.ErrCorrupt},
		{"odd hex", head + defs + `{"t":"s","s":1,"at":5,"req":"030","resp":"03"}` + "\n", 0, capture.ErrCorrupt},
		{"negative time", head + defs + `{"t":"s","s":1,"at":-5,"req":"03","resp":"03"}` + "\n", 0, capture.ErrCorrupt},
		{"fractional time", head + defs + `{"t":"s","s":1,"at":5.5,"req":"03","resp":"03"}` + "\n", 0, capture.ErrCorrupt},
		{"port out of range", head + `{"t":"target","id":1,"port":70000,"unit":1}` + "\n", 0, capture.ErrCorrupt},
		{"unknown transport", head + `{"t":"target","id":1,"transport":"carrier-pigeon","unit":1}` + "\n", 0, capture.ErrCorrupt},
		{"undefined stream", head + `{"t":"s","s":1,"at":5,"req":"03","resp":"03"}` + "\n", 0, capture.ErrCorrupt},
		{"record after the end", head + `{"t":"end","at":5}` + "\n" + defs, 0, capture.ErrCorrupt},
		{"not JSON", head + "hello\n", 0, capture.ErrCorrupt},
		{"last line cut", head + defs + `{"t":"s","s":1,"at":5,"req":"03","re`, 0, capture.ErrTruncated},
		{"last line without newline", head + `{"t":"end","at":5}`, 0, capture.ErrTruncated},
		{"empty input", "", 0, capture.ErrTruncated},
		{"too long a line", head + `{"t":"note","x":"` + strings.Repeat("a", MaxLineLen) + `"}` + "\n", 0, capture.ErrCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := read(tc.in)
			if tc.err == nil {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if len(c.Samples) != tc.samples {
					t.Fatalf("%d samples, want %d", len(c.Samples), tc.samples)
				}
				return
			}
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
		})
	}
}

// Errors name the line they are on.
func TestErrorLine(t *testing.T) {
	_, err := read(head + defs + `{"t":"s","s":9,"at":5,"req":"03","resp":"03"}` + "\n")
	if err == nil || !strings.Contains(err.Error(), "line 4") {
		t.Fatalf("err = %v, want it to name line 4", err)
	}
}
