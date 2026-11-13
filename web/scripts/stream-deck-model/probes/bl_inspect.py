import bpy,sys
from mathutils import Vector
bpy.ops.wm.read_factory_settings(use_empty=True)
bpy.ops.import_scene.gltf(filepath=sys.argv[sys.argv.index('--')+1])
tot=0
rows=[]
for o in bpy.data.objects:
    if o.type!='MESH': 
        print('NONMESH',o.name,o.type,o.parent.name if o.parent else None); continue
    n=len(o.data.polygons); tris=sum(len(p.vertices)-2 for p in o.data.polygons); tot+=tris
    bb=[o.matrix_world@Vector(c) for c in o.bound_box]
    mn=[min(v[i] for v in bb) for i in range(3)];mx=[max(v[i] for v in bb) for i in range(3)]
    rows.append((tris,o.name,[m.name if m else None for m in o.data.materials],[round(x,4) for x in mn],[round(x,4) for x in mx],len(o.data.uv_layers)))
rows.sort(reverse=True)
print('TOTAL tris',tot,'objects',len(rows))
for r in rows[:60]: print(r)
