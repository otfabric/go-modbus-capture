# Support and feature matrix

What go-modbus-capture supports and how it is tested. Status: v0.0.2, format draft 0.1. Out of scope: [LIMITATIONS.md](../LIMITATIONS.md).

None of this is a conformance claim: nothing is certified, and MBCAP is not a standard of any body.

## 1. Versions

| go-modbus-capture | Go | Format | Supported |
|---|---|---|---|
| **v0.0.2** | 1.23 or later (CI: 1.23 to 1.26) | draft 0.1 | **yes** |

## 2. Platforms

Pure Go with the standard library only. CI tests on Linux amd64. Linux arm64 and arm, macOS arm64 and Windows amd64 are built by `make cross`, not tested.

## 3. Feature support

✅ supported · ◐ partly · — not implemented. **Independent**: no independent MBCAP implementation exists, so that column would be empty for every row and is left out.

| Feature | Read | Write | Tests |
|---|:---:|:---:|---|
| Binary `.mbcap` (blocks, deflate, checksums, index) | ✅ | ✅ | golden, fuzz |
| `mbcap.Repair` of a file cut short | ✅ | ✅ | unit |
| JSON Lines `.mbcap.jsonl` | ✅ | ✅ | unit, fuzz |
| Long CSV `.mbcap.csv` | ✅ | ✅ | unit, fuzz |
| Wide CSV | — | ◐ a one-way view | unit |
| Filter, trim, split, merge (`edit`) | n/a | ✅ | unit |
| Read function codes in a capture | ✅ | ✅ | unit |
| Write, vendor, user-defined and unknown function codes | — | — | refused as invalid |
| Decoded values, MCAP v1 | — | — | n/a |
