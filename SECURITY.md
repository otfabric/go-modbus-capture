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
- The readers are fuzzed (`make fuzz`: `mbcap` `FuzzReader` and `FuzzRoundTrip`, `jsonl` and `mbcsv` `FuzzReader`)
- The library and `mbcapctl` never open a network connection; they read and write files only
