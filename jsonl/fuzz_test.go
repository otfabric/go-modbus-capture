// SPDX-License-Identifier: MIT

package jsonl

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/fuzzcap"
)

var codec = fuzzcap.Codec{
	NewReader: func(r io.Reader) (capture.Reader, error) { return NewReader(r) },
	NewWriter: func(w io.Writer, h capture.Header) (capture.Writer, error) { return NewWriter(w, h) },
}

// Every field name of the format, and those of its flags and serial objects.
var (
	lineNames = []string{"t", "version", "tick_ns", "wall_start", "producer", "flags", "tags",
		"id", "label", "transport", "host", "port", "serial", "unit", "target", "role", "s", "at", "req", "resp"}
	innerNames = map[string][]string{
		"flags":  {"change_only", "exceptions"},
		"serial": {"device", "baud", "parity", "data_bits", "stop_bits"},
	}
)

// only returns the members of obj with one of names, by exact name.
func only(obj map[string]json.RawMessage, names []string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, n := range names {
		if v, ok := obj[n]; ok {
			out[n] = v
		}
	}
	return out
}

// keyExact rewrites every line of data that is a JSON object with only the
// fields of the format, picked from a map by their exact names: another way
// to leave out unknown fields than the reader's.
func keyExact(data []byte) []byte {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		var obj map[string]json.RawMessage
		if !bytes.HasSuffix(line, []byte("\n")) || json.Unmarshal(line, &obj) != nil || obj == nil {
			out.Write(line)
			continue
		}
		obj = only(obj, lineNames)
		for name, names := range innerNames {
			var inner map[string]json.RawMessage
			if json.Unmarshal(obj[name], &inner) == nil && inner != nil {
				obj[name], _ = json.Marshal(only(inner, names))
			}
		}
		_ = enc.Encode(obj)
	}
	return out.Bytes()
}

// membersByDecoder splits a JSON object into its members with the token
// reader of encoding/json: what members must agree with.
func membersByDecoder(data []byte) (names []string, values [][]byte, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, _ := dec.Token(); tok != json.Delim('{') {
		return nil, nil, false
	}
	for dec.More() {
		name, _ := dec.Token()
		var value json.RawMessage
		_ = dec.Decode(&value)
		names = append(names, name.(string))
		values = append(values, value)
	}
	return names, values, true
}

// checkMembers compares members with membersByDecoder for one valid JSON
// text.
func checkMembers(t *testing.T, data []byte) {
	t.Helper()
	ms, ok := members(nil, data)
	names, values, wantOK := membersByDecoder(data)
	if ok != wantOK || len(ms) != len(names) {
		t.Fatalf("members(%q): %d members, object=%v; encoding/json reads %d, object=%v", data, len(ms), ok, len(names), wantOK)
	}
	for i, m := range ms {
		// encoding/json puts U+FFFD for every byte of a name that is not
		// valid UTF-8, as the conversion to runes does.
		if name := string([]rune(string(m.name))); name != names[i] || !bytes.Equal(m.value, values[i]) {
			t.Fatalf("members(%q): member %d is %q: %q; encoding/json reads %q: %q", data, i, m.name, m.value, names[i], values[i])
		}
	}
}

// members splits any valid JSON object as encoding/json does.
func TestMembers(t *testing.T) {
	for _, in := range []string{
		`{}`, ` { } `, `{"a":1}`, "\t{\r\n\"a\" :\t1 ,\n\"b\":2\n}\n",
		`{"a":"x","a":"y","A":"z"}`,
		`{"":0,"a b":-1.5e+10,"c":true,"d":false,"e":null}`,
		`{"s":"with \"quotes\", a brace } a bracket ] and a comma ,","t":"\\"}`,
		`{"o":{"p":{"q":[1,{"r":"}]"},[[]],"]"]}},"after":1}`,
		`{"a":[],"b":{},"c":[{}],"d":[[],[]]}`,
		`{"\u0074":"s","\"":1,"\\":2,"a\nb":3,"\u00e9\ud83d\ude00":4}`,
		`{"ünï":"中","key":"\u0000\u001f"}`,
		`{"n":0}`, `{"n":-0.0E-0 }`, `{"n":123456789012345678901234567890}`,
		`[1,2]`, `"str"`, `5`, `null`, `true`, ` [ {"a":1} ] `,
		"{\"bad utf8 \xff\xfe\":1,\"\xc3\":2}",
	} {
		if !json.Valid([]byte(in)) {
			t.Fatalf("%q is not valid JSON", in)
		}
		checkMembers(t, []byte(in))
	}
	// A value ends where its text does when nothing follows it.
	for in, want := range map[string]int{`12`: 2, `12 `: 2, `null`: 4, `"a,b" `: 5, `{"a":[1,"}"]},`: 13, `[[],{}]`: 7} {
		if got := valueLen([]byte(in)); got != want {
			t.Errorf("valueLen(%q) = %d, want %d", in, got, want)
		}
	}
}

func FuzzReader(f *testing.F) {
	for _, s := range fuzzcap.Seeds(f, "*.mbcap.jsonl") {
		f.Add(s)
	}
	f.Add([]byte(`{"t":"header","version":"0.1","tick_ns":1,"wall_start":"1970-01-01T00:00:00Z"}` + "\n" + `{"t":"x","id":"not a number"}` + "\n"))
	// Field names in another case, and fields that repeat.
	f.Add([]byte(lines(fullHeader, fullTarget, fullStream, with(fullSample, `"AT":7`), fullEnd)))
	f.Add([]byte(lines(fullHeader, with(fullTarget, `"Label":"other","Serial":{"device":"d"}`), with(fullStream, `"ROLE":"scan"`), fullSample,
		`{"t":"note","T":"s","s":1,"at":6,"req":"0300000001","resp":"03020008"}`, with(fullEnd, `"AT":8`))))
	f.Add([]byte(lines(with(fullHeader, `"Version":"9.9","Flags":{"change_only":true}`), with(fullTarget, `"ID":9,"Serial":{"device":"d"}`),
		with(fullStream, `"Target":9`), with(fullSample, `"AT":99,"S":9,"REQ":"0400000001","Resp":"0302ffff"`),
		`{"t":"note","T":"s","s":1,"at":6,"req":"0300000001","resp":"03020008"}`, with(fullEnd, `"AT":99999`))))
	f.Add([]byte(lines(fullHeader, fullTarget, fullStream, with(fullSample, `"note":1,"note":2`), with(fullSample, `"at":6`))))
	f.Add([]byte(lines(fullHeader, with(fullTarget, `"flags":1`)) + `{"t":"target","id":2,"serial":{"Baud":1,"baud":2,"x":{"baud":3}},"unit":1}` + "\n"))
	f.Add([]byte(`{"\u0074":"header","o":{"p":[1,{"r":"}]"}]},"s":"a \"b\" }"}` + "\n" + ` [ {"a":1} ] ` + "\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		// Whatever the lines hold: the reader splits a line that is valid
		// JSON into the members encoding/json reads.
		for line := range bytes.SplitSeq(data, []byte("\n")) {
			if json.Valid(line) {
				checkMembers(t, line)
			}
		}
		got := fuzzcap.ReadAccepted(codec, data)
		if got == nil {
			return
		}
		fuzzcap.CheckRoundTrip(t, codec, got)
		// The reader takes a field by its exact name only: without the
		// other fields the capture is the same.
		exact := fuzzcap.ReadAccepted(codec, keyExact(data))
		if exact == nil {
			t.Fatalf("the capture does not read from its defined fields alone")
		}
		if d := capture.Diff(got, exact); d != "" {
			t.Fatalf("the reader used a field that is not defined by its exact name: %s", d)
		}
	})
}
