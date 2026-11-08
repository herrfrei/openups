#!/usr/bin/env python3
"""Generate openups_golden.json from the pyopenups reference implementation.

    PYOPENUPS_SRC=/path/to/pyopenups-main/src python3 gen_golden.py > openups_golden.json

The Go codec (config.go) is tested against this file, so a divergence from the
Python/C++ behaviour shows up without any hardware. Needs pyusb installed
(pyopenups imports it at module load).
"""
import json, os, random, sys

sys.path.insert(0, os.environ.get("PYOPENUPS_SRC", "pyopenups-main/src"))
from pyopenups.ups import openups as o

u = o.HIDOpenUPS(None)
msgs = [(i, m) for i, m in enumerate(u.get_messages()) if m is not None and m.nLen]

rng = random.Random(1234)
decode = []

def raw_cases(m):
    n = m.nLen
    cases = [bytes(n), bytes([0xFF] * n), bytes([1] + [0] * (n - 1)),
             bytes([0x10] * n), bytes([0x7F] * n)]
    for _ in range(12):
        cases.append(bytes(rng.randrange(256) for _ in range(n)))
    if m.nReadMode == 11:   # make sure every valid selector appears
        for t in o.G_OPENUPS_MEM_UVP:
            cases.append(bytes([t.nCVR_2, t.nCVR_1]))
    if m.nReadMode == 15:   # sweep the thermistor table and between its steps
        for c in o.G_OPENUPS_MEM_TERM:
            cases += [c.to_bytes(2, "little"), (c + 1).to_bytes(2, "little")]
    if m.nReadMode == 7:
        cases += [bytes([b, 0]) for b in range(0, 256, 5)]
    return cases

for i, m in msgs:
    for raw in raw_cases(m):
        u.m_chPackages[:] = bytes(len(u.m_chPackages))
        u.m_chPackages[m.nIndex:m.nIndex + m.nLen] = raw
        _, name, value, _, _ = u.get_ups_variable_data(i)
        decode.append({"name": m.strName, "raw": raw.hex(), "want": value})

json.dump({"decode": decode}, sys.stdout, separators=(",", ":"))
print()
