# Interoperability

No independent implementation of MBCAP exists, so nothing here has been tested against one. MBCAP is an OT Fabric format, not a standard of any body, and nothing is certified.

The format is tested by this repository's golden files, unit tests and fuzz targets. Two OT Fabric programs read or write it. They share this code, so they are not independent evidence:

| Program | Use | Evidence |
|---|---|---|
| go-modbus-trace v0.2.0 | writes MBCAP from a packet trace, using this module at v0.0.2 | its `TestReplayEndToEnd` checks the files with a pinned `mbcapctl verify` |
| modbusctl | serves a capture as a virtual device | same test, in go-modbus-trace |

Files written now will not be readable by format 1.0 ([LIMITATIONS.md](LIMITATIONS.md)).
