"""Verify STEP round trip and watertight, bed-aligned binary STL exports."""
import json
import struct
from collections import Counter
from pathlib import Path
from build123d import import_step

out=Path(__file__).resolve().parent/'output'
report=json.loads((out/'validation.json').read_text())
checks={}
for name,part in report['parts'].items():
    data=(out/f'{name}.stl').read_bytes()
    count=struct.unpack_from('<I',data,80)[0]
    assert len(data)==84+50*count,(name,'truncated STL')
    edges=Counter(); zs=[]
    for i in range(count):
        f=struct.unpack_from('<12fH',data,84+50*i)
        vs=[tuple(round(v,4) for v in f[j:j+3]) for j in (3,6,9)]
        assert len(set(vs))==3,(name,'degenerate triangle')
        zs += [v[2] for v in vs]
        for j in range(3):edges[tuple(sorted((vs[j],vs[(j+1)%3])))]+=1
    assert all(n==2 for n in edges.values()),(name,'non-manifold STL edges')
    assert abs(min(zs))<.001,(name,'not on print bed')
    checks[name]={'triangles':count,'watertight_edges':True,'min_z_mm':min(zs)}
step=import_step(out/'assembly.step')
assert step.is_valid and len(step.solids())==len(report['parts'])
expected=sum(p['volume_mm3'] for p in report['parts'].values())
assert abs(step.volume-expected)<1,'STEP volume round-trip error'
(out/'export-checks.json').write_text(json.dumps({'step_valid':True,'step_solids':len(step.solids()),'stl':checks},indent=2)+'\n')
print(f'PASS: {len(checks)} watertight, bed-aligned STLs; STEP contains {len(step.solids())} valid solids.')
