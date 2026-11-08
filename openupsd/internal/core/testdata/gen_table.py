"""Rebuild params_openups.go:

    PYOPENUPS_SRC=/path/to/pyopenups-main/src python3 gen_table.py | gofmt > ../params_openups.go
"""
import sys, json, os
sys.path.insert(0,os.environ.get('PYOPENUPS_SRC','pyopenups-main/src'))
from pyopenups.ups import openups as o
u=o.HIDOpenUPS(None)

out=[]
for i,m in enumerate(u.get_messages()):
    if m is None or m.nLen==0: continue
    desc=m.strText.replace("\r\n","\n")
    out.append(dict(i=i,idx=m.nIndex,len=m.nLen,name=m.strName,mode=m.nReadMode,mult=m.dMultiplier,
                    unit=m.strUnit,desc=desc))
def q(s): return json.dumps(s, ensure_ascii=False)
def f(x):
    r=repr(float(x)); return r
lines=["// The parameter table below was derived from pyopenups' HIDOpenUPS message",
"// table (itself a port of the Mini-Box C++ tool) by testdata/gen_table.py.",
"","package core","",
"// OpenUPSParams is the OpenUPS flash configuration table, in the order used by","// the Mini-Box C++ tool (and therefore by config dumps).",
"var OpenUPSParams = []Param{"]
for p in out:
    lines.append("\t{Name: %s, Index: %d, Len: %d, Mode: %d, Mult: %s, Unit: %s,\n\t\tDesc: %s},"%(
      q(p['name']),p['idx'],p['len'],p['mode'],f(p['mult']),q(p['unit']),q(p['desc'])))
lines.append("}")
sys.stdout.write("\n".join(lines)+"\n")

