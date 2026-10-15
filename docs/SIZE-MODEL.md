# Size model (SYNTHETIC data)

> **Audience: contributors.** How small the binary encoding is, estimated on synthetic data. The encoding
> itself is described in [Encodings](encodings.md).

Produced by `docs/sizemodel/bench.py`: a synthetic poller (three FC03 blocks of 62, 67 and 120 registers, 3,600 polls,
10,800 samples) and a **sketch** of the MBCAP coding (no latency, one timestamp per sample, 256-sample blocks with
independent state, per-block deflate). The data is generated, so read these as the shape of the saving, not as
predictions for real devices. The script sizes the encoding; it is not a working encoder.

Bytes per sample (10,800 samples; each poll reads three blocks). "Raw PDUs" is the request and response PDUs of a
sample, 173 bytes on average, as a reference point:

| Scenario | raw PDUs | MBCAP full | full + deflate | change-only | change-only + deflate |
|---|---|---|---|---|---|
| Mostly static: 95% static, 4% slow counters, 1% walking | 173.0 | 7.7 | 5.8 | 1.1 | 0.9 |
| Typical meter: 60% static, 25% slow, 13% walking, 2% noisy | 173.0 | 36.7 | 24.7 | 36.7 | 24.7 |
| Busy device: 30% static, 20% slow, 30% walking, 20% noisy | 173.0 | 117.8 | 89.9 | 117.8 | 89.9 |
| Worst case: every register noisy | 173.0 | 173.0 | 172.8 | 173.0 | 172.8 |

Observations:
- Typical row: MBCAP is 4.7× smaller than the raw PDUs before compression and 7× with per-block deflate.
- Change-only pays off when whole blocks stay unchanged between polls (first row). In the typical row some register changes
  on every poll, so every sample is stored and the two modes are equal.
- Pure noise cannot be compressed, as expected: the cost is that of the raw PDUs.
- Framing + timing per sample in the sketch: roughly 4–6 bytes (flags, stream, request ref, dt varint).

## Measured with the Go encoder

`go test ./mbcap -run TestSizeModel -v` encodes the same scenarios (Go generator `internal/synth`, not the
Python one, so the random data differs) with the real `mbcap` writer: CRC per block, templates, response
lengths and the index included. Bytes per sample:

| Scenario | full | full + deflate | change-only | change-only + deflate |
|---|---|---|---|---|
| Mostly static | 8.9 | 6.7 | 4.1 | 2.7 |
| Typical meter | 39.6 | 28.5 | 39.6 | 28.4 |
| Busy device | 119.8 | 95.9 | 119.8 | 95.9 |
| Worst case | 175.1 | 175.1 | 175.1 | 175.1 |

The full-capture rows land within about 15% of the sketch (framing the sketch leaves out). The mostly static
change-only row is well above the sketch's (2.7 against 0.9 bytes per sample): the Go generator stores more samples
there. Both generators are synthetic and draw different random data, so this is not a finding about the coding;
real captures settle it.

**Next step:** replace this with measurements on real captures before freezing the coding (REQUIREMENTS section 15).
