// SPDX-License-Identifier: MIT

// Package synth generates synthetic captures for tests and benchmarks: a
// poller reading blocks of holding registers whose values are static, slow
// counters, walking or noisy (after docs/sizemodel/bench.py), plus optional
// extras covering every read function, exceptions, several targets and
// samples written slightly out of order.
package synth

import (
	"encoding/binary"
	"math/rand"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
)

// Mix gives the share of registers of each kind. The shares need not sum to
// 1; they are weights.
type Mix struct{ Static, Slow, Walk, Noisy float64 }

// Mixes of docs/SIZE-MODEL.md.
var (
	MostlyStatic = Mix{0.95, 0.04, 0.01, 0}
	Typical      = Mix{0.60, 0.25, 0.13, 0.02}
	Busy         = Mix{0.30, 0.20, 0.30, 0.20}
	AllNoisy     = Mix{0, 0, 0, 1}
)

// Block is a range of holding registers read with one FC03 request.
type Block struct{ Start, Count uint16 }

// DefaultBlocks are the three blocks of the size model.
var DefaultBlocks = []Block{{4096, 62}, {4158, 67}, {5000, 120}}

// Config describes a synthetic capture.
type Config struct {
	Seed   int64
	Polls  int
	Mix    Mix
	Blocks []Block // default DefaultBlocks
	// ChangeOnly drops samples whose response equals the previous one.
	ChangeOnly bool
	// Extras adds a second target with one sample stream per read function
	// (FC01, 02, 04, 07, 08, 11, 12, 17, 20, 24, a vendor function), an FC43
	// identity stream read in two pages, exception responses, and samples
	// of concurrent streams written out of time order.
	Extras bool
}

// Generate returns the capture described by cfg.
func Generate(cfg Config) *capture.Capture {
	rng := rand.New(rand.NewSource(cfg.Seed))
	blocks := cfg.Blocks
	if blocks == nil {
		blocks = DefaultBlocks
	}
	c := &capture.Capture{Header: capture.Header{
		TickNS:     capture.DefaultTickNS,
		WallStart:  time.Date(2026, 10, 14, 13, 14, 24, 701149000, time.UTC),
		ChangeOnly: cfg.ChangeOnly,
		Exceptions: cfg.Extras,
		Producer:   "synth",
		Tags:       map[string]string{"site": "lab"},
	}}
	c.Targets = append(c.Targets, capture.Target{ID: 1, Label: "Inverter A", Transport: capture.TransportTCP,
		Host: "192.168.2.201", Port: 502, Unit: 21})
	c.Streams = append(c.Streams, capture.Stream{ID: 1, Target: 1, Role: capture.RolePoll, Label: "holding blocks"})

	type reg struct {
		kind int
		v    uint16
	}
	w := []float64{cfg.Mix.Static, cfg.Mix.Slow, cfg.Mix.Walk, cfg.Mix.Noisy}
	// Registers in address order, so that the generator is deterministic.
	var order []*reg
	regs := map[uint16]*reg{}
	for _, b := range blocks {
		for a := b.Start; a < b.Start+b.Count; a++ {
			r := &reg{kind: pick(rng, w), v: uint16(rng.Intn(65536))}
			regs[a] = r
			order = append(order, r)
		}
	}

	last := map[string][]byte{}
	add := func(stream uint32, at capture.Ticks, req, resp []byte) {
		if resp[0]&0x80 != 0 && !c.Header.Exceptions {
			return
		}
		if cfg.ChangeOnly {
			k := string(rune(stream)) + string(req)
			if p, ok := last[k]; ok && string(p) == string(resp) {
				return
			}
			last[k] = resp
		}
		c.Samples = append(c.Samples, capture.Sample{Stream: stream, At: at, Request: req, Response: resp})
	}

	if cfg.Extras {
		c.Targets = append(c.Targets, capture.Target{ID: 2, Label: "Meter on RS-485", Transport: capture.TransportRTU,
			Serial: capture.Serial{Device: "/dev/ttyUSB0", Baud: 19200, Parity: capture.ParityEven, DataBits: 8, StopBits: 1},
			Unit:   3, Tags: map[string]string{"bus": "A"}})
		c.Streams = append(c.Streams,
			capture.Stream{ID: 2, Target: 2, Role: capture.RolePoll, Label: "every read function"},
			capture.Stream{ID: 3, Target: 2, Role: capture.RoleIdentity},
		)
	}

	var t capture.Ticks
	for it := 0; it < cfg.Polls; it++ {
		for _, r := range order {
			switch r.kind {
			case 1:
				if rng.Float64() < 0.02 {
					r.v++
				}
			case 2:
				r.v += uint16(rng.Intn(7) - 3)
			case 3:
				r.v = uint16(rng.Intn(65536))
			}
		}
		for bi, b := range blocks {
			if bi == 0 {
				t += capture.Ticks(1_000_000 - 120_000 + rng.Intn(10_001) - 5000)
			} else {
				t += capture.Ticks(500 + rng.Intn(2501))
			}
			req := []byte{0x03, byte(b.Start >> 8), byte(b.Start), byte(b.Count >> 8), byte(b.Count)}
			resp := []byte{0x03, byte(2 * b.Count)}
			for a := b.Start; a < b.Start+b.Count; a++ {
				resp = binary.BigEndian.AppendUint16(resp, regs[a].v)
			}
			add(1, t, req, resp)
		}
		if cfg.Extras {
			extras(rng, it, t, add)
		}
	}
	end := t + 1000
	for _, s := range c.Samples {
		end = max(end, s.At+1)
	}
	c.End = capture.End{Known: true, At: end}
	return c
}

func pick(rng *rand.Rand, w []float64) int {
	sum := 0.0
	for _, x := range w {
		sum += x
	}
	x := rng.Float64() * sum
	for i, v := range w {
		if x < v {
			return i
		}
		x -= v
	}
	return len(w) - 1
}

// extras adds one round of the extra streams around time t.
func extras(rng *rand.Rand, it int, t capture.Ticks, add func(uint32, capture.Ticks, []byte, []byte)) {
	at := func(d int) capture.Ticks {
		// Concurrent streams: up to 3 ms out of order, never before 0.
		if v := int64(t) + int64(d) - int64(rng.Intn(3000)); v > 0 {
			return capture.Ticks(v)
		}
		return 0
	}
	bits := byte(rng.Intn(256))
	add(2, at(100), []byte{0x01, 0x00, 0x00, 0x00, 0x0A}, []byte{0x01, 0x02, bits, bits & 0x03})
	add(2, at(200), []byte{0x02, 0x00, 0x10, 0x00, 0x03}, []byte{0x02, 0x01, bits & 0x07})
	add(2, at(300), []byte{0x04, 0x00, 0x00, 0x00, 0x02}, []byte{0x04, 0x04, 0x00, byte(it), 0x12, 0x34})
	add(2, at(400), []byte{0x07}, []byte{0x07, bits})
	add(2, at(500), []byte{0x08, 0x00, 0x00, 0xA5, 0x37}, []byte{0x08, 0x00, 0x00, 0xA5, 0x37})
	add(2, at(600), []byte{0x08, 0x00, 0x0B, 0x00, 0x00}, []byte{0x08, 0x00, 0x0B, 0x00, byte(it)})
	add(2, at(700), []byte{0x0B}, []byte{0x0B, 0x00, 0x00, 0x00, byte(it)})
	add(2, at(800), []byte{0x0C}, []byte{0x0C, 0x08, 0x00, 0x00, 0x01, 0x08, 0x00, byte(it), 0x20})
	add(2, at(900), []byte{0x11}, []byte{0x11, 0x05, 'M', 'T', 'R', 0xFF, 0x01})
	add(2, at(1000), []byte{0x14, 0x07, 0x06, 0x00, 0x04, 0x00, 0x01, 0x00, 0x02},
		[]byte{0x14, 0x06, 0x05, 0x06, 0x0D, 0xFE, 0x00, byte(it)})
	add(2, at(1100), []byte{0x18, 0x04, 0xDE}, []byte{0x18, 0x00, 0x06, 0x00, 0x02, 0x01, 0xB8, 0x12, byte(it)})
	add(2, at(1200), []byte{0x41, 0x01, 0x02}, []byte{0x41, 0x0A, 0x0B, byte(it)}) // a vendor function
	if it%5 == 0 {
		add(2, at(1300), []byte{0x04, 0x27, 0x10, 0x00, 0x01}, []byte{0x84, 0x02}) // an exception
	}
	if it%10 == 0 {
		add(3, at(1400), []byte{0x2B, 0x0E, 0x01, 0x00},
			[]byte{0x2B, 0x0E, 0x01, 0x01, 0xFF, 0x02, 0x02, 0x00, 0x04, 'A', 'C', 'M', 'E', 0x01, 0x02, 'X', '1'})
		add(3, at(1500), []byte{0x2B, 0x0E, 0x01, 0x02},
			[]byte{0x2B, 0x0E, 0x01, 0x01, 0x00, 0x00, 0x01, 0x02, 0x03, '1', '.', '0'})
	}
}
