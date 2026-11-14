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
or CSV row, 4 096 fields per CSV row and 65 536 samples per binary data block.

- **A binary file holds about 1.7 million data blocks.** Its index is one
  block of at most 32 MiB. With the default of 256 samples per block that is
  about 440 million samples; a recorder that calls `Flush` once per second
  fills the index in about 20 days. The writer returns an error for the sample
  that no longer fits and the file still closes. Continuing in a new file is
  up to the recorder: nothing here rotates files.
- **`edit.Split` and `mbcapctl split` write at most 4 096 parts.** A capture
  with more targets or streams than that is split in steps, a selection at a
  time (`edit.Filter`, `mbcapctl filter`). The number is fixed: there is no
  option for it.
- **The format version** of a binary file has a minor version of at most
  2^31-1.

## Memory

- A binary reader holds one decoded data block at a time: up to 65 536
  samples, each with its own request and response PDU, which is about 40 MiB
  when every PDU has the largest size. The size of the block in the file says
  little about it: repeated responses take a few bytes there.
- `capture.ReadAll` holds the whole capture, and so do `edit.Merge`,
  `mbcapctl merge`, `mbcapctl cat` and `mbcapctl convert` to wide CSV: memory
  in proportion to the number of samples and the size of their PDUs, not to
  the size of the file. Nothing limits it. The other operations and commands
  work record by record.
- `edit.Split` keeps every part open until the input ends: up to 4 096
  writers, and in `mbcapctl split` as many open files. A binary part that
  compresses holds about 1 MiB from its first data block until it is closed;
  a closed part holds nothing.

## Verification

- Tested with this repository's own golden files, unit tests and fuzz targets.
  No independent implementation of MBCAP exists to test against.
- Certification: none. MBCAP is an OT Fabric format, not a standard of any body.
