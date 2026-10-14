"""Revised size model (SYNTHETIC data): no latency, one timestamp per sample.

v1: exact layout of modbusctl/internal/mcap/codec.go (26 B per record + data, JSON header).
MBCAP record = flags(1) + stream(1) + request ref(1) + zigzag varint dt + response coding
            (RAW len+PDU | SAME nothing | DIFF sparse changed 16-bit words).
Modes: 'full'    = every sample stored (keeps the poll cadence),
       'changes' = a sample identical to the previous sample of the same request is not stored.
Blocks of 256 records are independently decodable (state reset), deflate applied per block.
"""
import json, lzma, random, struct, zlib

def varint(n):
    out = bytearray()
    while True:
        b = n & 0x7F
        n >>= 7
        if n:
            out.append(b | 0x80)
        else:
            out.append(b)
            return bytes(out)

def zz(n):
    return varint((n << 1) ^ (n >> 63))

BLOCKS = [(4096, 62), (4158, 67), (5000, 120)]

def run(mix, changes_only, iterations=3600, seed=1, blk=256):
    rng = random.Random(seed)
    regs = {}
    for start, cnt in BLOCKS:
        for a in range(start, start + cnt):
            regs[a] = [rng.choices(["static", "slow", "walk", "noisy"], mix)[0], rng.randrange(65536)]
    hdr = json.dumps({"IP": "192.168.2.201", "Port": 502, "Unit": 21, "Function": 3, "StartTime": 1752498356998694708}).encode()
    v1 = bytearray(b"MCAP\x01" + struct.pack(">H", len(hdr)) + hdr)
    blocks_out, cur, prev, n_in_block, t_us, t_prev = [], None, {}, 0, 0, 0
    stored = 0
    for it in range(iterations):
        for a, r in regs.items():
            k, v = r
            if k == "slow" and rng.random() < 0.02: r[1] = (v + 1) & 0xFFFF
            elif k == "walk": r[1] = (v + rng.randint(-3, 3)) & 0xFFFF
            elif k == "noisy": r[1] = rng.randrange(65536)
        for bi, (start, cnt) in enumerate(BLOCKS):
            t_us += rng.randint(500, 3000) if bi else 1_000_000 - 120_000 + rng.randint(-5000, 5000)
            data = b"".join(struct.pack(">H", regs[a][1]) for a in range(start, start + cnt))
            v1 += struct.pack(">IqqHHH", it, t_us * 1000, (t_us + 30000) * 1000, start, cnt, len(data)) + data
            if n_in_block == 0:
                prev = {}
                cur = bytearray(); blocks_out.append(cur)
            old = prev.get(bi)
            mode, body = 0, data  # RAW payload
            if old is not None and len(old) == len(data):
                if old == data:
                    mode, body = 1, b""
                else:
                    diffs, last, n = bytearray(), 0, 0
                    for w in range(len(data) // 2):
                        if data[2*w:2*w+2] != old[2*w:2*w+2]:
                            diffs += varint(w - last) + data[2*w:2*w+2]; last = w; n += 1
                    sparse = varint(n) + bytes(diffs)
                    if len(sparse) < len(data): mode, body = 2, sparse
            prev[bi] = data
            if mode == 1 and changes_only:
                continue                      # unchanged sample is not stored
            if mode == 0: body = varint(len(data) + 1) + data   # RAW: length + PDU (fc + data approx.)
            rec = bytes([mode << 6, 1, bi]) + zz(t_us - t_prev) + body
            t_prev = t_us
            cur += rec
            stored += 1
            n_in_block = (n_in_block + 1) % blk
    v2 = b"\x89MBCAP\r\n" + b"\x00" * 60 + b"".join(bytes(b) + b"\x00" * 6 for b in blocks_out)
    perblock = 76 + sum(len(zlib.compress(bytes(b), 9)) + 6 for b in blocks_out)
    return len(v1), len(zlib.compress(bytes(v1), 9)), len(lzma.compress(bytes(v1))), len(v2), perblock, len(lzma.compress(v2)), stored

polls = 3600 * 3
mixes = [
    ("mostly static (95% static, 4% slow, 1% walking)", [.95, .04, .01, 0]),
    ("typical meter (60/25/13/2)", [.60, .25, .13, .02]),
    ("busy (30/20/30/20)", [.30, .20, .30, .20]),
    ("all noisy", [0, 0, 0, 1]),
]
print(f"bytes per sample ({polls} samples); stored = records written in MBCAP")
print(f"{'scenario':<52}{'v1':>7}{'v1+xz':>7}{'full':>7}{'full+blk':>9}{'chg':>7}{'chg+blk':>9}{'stored(chg)':>13}")
for name, mix in mixes:
    a = run(mix, False); b = run(mix, True)
    print(f"{name:<52}{a[0]/polls:7.1f}{a[2]/polls:7.1f}{a[3]/polls:7.1f}{a[4]/polls:9.1f}{b[3]/polls:7.1f}{b[4]/polls:9.1f}{b[6]:13d}")
