# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in `go-modbus-capture`, please report it responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Instead, please email **security@otfabric.io**, or open a private advisory on GitHub (the [Security](https://github.com/otfabric/go-modbus-capture/security) tab → **Advisories** → **Report a vulnerability**), with:

- A description of the vulnerability
- Steps to reproduce the issue, ideally a minimal program, test or capture file
- The affected version(s)
- Any potential impact you've identified

We will acknowledge your report within 48 hours and aim to provide a fix or mitigation within 7 days for critical issues.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| Older releases | Best effort |

## Security Considerations

### Capture files contain process data

An MBCAP capture, and its JSON Lines and CSV twins, stores what real devices answered: raw request and response PDUs with their timing, plus device addresses (host, port, unit id, serial device), labels, the producer and free-form tags. That can reveal plant layout, equipment identity and live process values.

- **Treat captures as sensitive operational data**: store and share them as you would the network they were recorded on
- Values are stored raw and are not anonymised by any tool here. `mbcapctl filter`, `trim` and `split` drop samples (and `filter` can drop targets) but keep the header, its tags and the full details of every target they keep; review a file before passing it on
- `mbcapctl` writes each output to a temporary file created with `os.CreateTemp` (mode 0600) and renames it into place, so new files are readable by their owner only until you change that
- Edits record their steps in the `provenance` header tag, and `merge` records the input file names in the `source` tag

### Integrity, not authenticity

- Blocks of the binary encoding carry a CRC32C. It detects accidental corruption, not tampering: anyone who can write a file can produce a valid one. Sign or hash captures separately if you need to prove where they came from

### Input handling

- Every reader treats its input as hostile: the binary reader bounds block sizes (`mbcap.MaxBlockLen`, `mbcap.MaxBlockRaw`), template counts and every length field; the JSON Lines and CSV readers bound line and record length (`jsonl.MaxLineLen`, `mbcsv.MaxRecordLen`); PDUs are at most 253 bytes (`capture.MaxPDULen`). Malformed input is reported as an error
- The binary reader also bounds the samples of a data block (65 536), and the CSV reader the fields of a row (4 096). All limits: [docs/FORMAT.md](docs/FORMAT.md) section 1
- What a reader allocates follows the limits and the bytes it has read, not the lengths and counts a file declares: a block or a compressed body of more than 64 KiB is read into a buffer that grows as the bytes arrive, the binary reader compares the block count of an index with the data blocks it read before it decodes an entry, and a CSV row ends at its field limit before it is taken in. What a reader and the whole-capture operations hold in memory, and the 4 096 parts of a split: [LIMITATIONS.md](LIMITATIONS.md#memory)
- The JSON Lines reader takes a field by its exact name only, and a defined field that appears twice in a line is an error: a file reads the same here as with any reader of the specification ([docs/FORMAT.md](docs/FORMAT.md) section 3)
- `mbcapctl info` and `dump` print the strings of a capture (producer, tags, labels, roles, hosts, serial devices) as text: a string with a control character, a line break or another character that is not printable is printed as a quoted Go string with escapes. Error messages, which may quote such a string, are printed on one line with the same escapes, and `info --json` writes those characters as `\u` escapes. `convert` and the other commands that write captures keep every string as it is
- The readers are fuzzed (`make fuzz` runs every fuzz target: `mbcap` `FuzzReader`, `FuzzBlocks` and `FuzzRoundTrip`, `jsonl` and `mbcsv` `FuzzReader`), and each limit has a test that measures what its refusal allocates ([TESTING.md](TESTING.md))
- The library and `mbcapctl` never open a network connection; they read and write files only
