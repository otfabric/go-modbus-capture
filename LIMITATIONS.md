# Limitations

What go-modbus-capture does not do, or does not yet promise. The non-goals in
full: [REQUIREMENTS.md](REQUIREMENTS.md) section 3.

## Scope not implemented

- **Capturing and replaying.** No recorder, replayer, Modbus client or server,
  and no network or serial I/O. These belong to
  [modbusctl](https://github.com/otfabric/modbusctl).
- **Writes.** Captures hold the read functions of the Modbus specification only.
  Write, vendor, user-defined and unknown function codes are invalid.
- **Decoding values.** Registers stay raw words, bits and PDUs. There is no
  decoded export and no export to third-party viewers.
- **Latency, retries, timeouts, link events and connection state** are not
  recorded.
- **MCAP v1** cannot be read.

## Format stability

- The format is draft 0.1: implemented, not frozen. It is frozen as 1.0 after
  the binary coding is calibrated on real captures, and 1.0 readers will not
  read 0.x files ([docs/FORMAT.md](docs/FORMAT.md)).
- The size numbers in [docs/SIZE-MODEL.md](docs/SIZE-MODEL.md) are from
  synthetic data ([REQUIREMENTS.md](REQUIREMENTS.md) section 15).
- Wide CSV is a one-way view. Binary, JSON Lines and long CSV round-trip
  losslessly.

## Input limits

Readers and writers refuse input beyond the limits in
[docs/FORMAT.md](docs/FORMAT.md) section 1, such as 4 MiB per JSON Lines line
or CSV row.

## Verification

- Tested with this repository's own golden files, unit tests and fuzz targets.
  No independent implementation of MBCAP exists to test against.
- Certification: none. MBCAP is an OT Fabric format, not a standard of any body.
