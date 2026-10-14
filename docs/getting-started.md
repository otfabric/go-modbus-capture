# Getting started

This page takes you from nothing to looking inside a capture, converting it, and reading it from Go.

## 1. Install the CLI

```bash
go install github.com/otfabric/go-modbus-capture/cmd/mbcapctl@latest
```

Or download a binary from the [releases](https://github.com/otfabric/go-modbus-capture/releases).

## 2. Get a capture

Captures are recorded by tools such as [`modbusctl`](https://github.com/otfabric/modbusctl). To follow
along without a device, use the examples in the repository's
[`testdata/`](https://github.com/otfabric/go-modbus-capture/tree/main/testdata), for example
`example.mbcap` (one inverter, four polls of two holding registers, one identity read).

## 3. Look inside

```bash
mbcapctl info example.mbcap
```

shows the targets, the streams, how many samples each has, how often they were polled, and which
function codes they use. To see every record:

```bash
mbcapctl dump example.mbcap
```

```
sample             0             +0s  s1   0310000002               -> 0304000101a7
sample       1000450       +1.00045s  s1   0310000002               -> 0304000101a7
sample       2001100        +2.0011s  s1   0310000002               -> 0304000101a8
```

Each sample is one answered request: `03 1000 0002` (read 2 holding registers at 4096) answered with
`03 04 0001 01a7`. See [Concepts](concepts.md) for what the fields mean.

## 4. Check it

```bash
mbcapctl verify example.mbcap
```

verifies checksums, structure and every rule of the model. A file cut short by a crash is reported as
truncated; `mbcapctl repair` turns it into a complete file.

## 5. Convert it

```bash
mbcapctl convert example.mbcap example.mbcap.jsonl
```

```bash
mbcapctl convert example.mbcap example.mbcap.csv
```

```bash
mbcapctl convert example.mbcap registers.wide.csv
```

The first two are lossless and convert back to the identical capture. The last is a chart-friendly view of
the raw register values over time. See [Encodings](encodings.md).

## 6. Cut it to size

```bash
mbcapctl filter site.mbcap inverter.mbcap --target 1
```

```bash
mbcapctl trim --from 10m --to 20m site.mbcap ten-minutes.mbcap
```

```bash
mbcapctl split --by stream --dir parts site.mbcap
```

See [the CLI reference](cli.md) for every command.

## 7. Read it from Go

```bash
go get github.com/otfabric/go-modbus-capture
```

```go
package main

import (
	"fmt"
	"io"
	"log"
	"os"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/mbcap"
)

func main() {
	f, err := os.Open("example.mbcap")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	r, err := mbcap.NewReader(f)
	if err != nil {
		log.Fatal(err)
	}
	h := r.Header()
	for {
		rec, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		if s, ok := rec.(capture.Sample); ok {
			fmt.Printf("%v stream %d: % x -> % x\n", h.Duration(s.At), s.Stream, s.Request, s.Response)
		}
	}
}
```

The [library guide](library.md) covers writing captures, errors, conversion and editing.
