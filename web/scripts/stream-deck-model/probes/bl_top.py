import bpy,sys,bmesh
from mathutils import Vector
bpy.ops.wm.read_factory_settings(use_empty=True)
bpy.ops.import_scene.gltf(filepath=sys.argv[sys.argv.index('--')+1])
for o in bpy.data.objects:
    if o.type!='MESH' or not o.name.startswith('WD050-REAR'): continue
    o.data.transform(o.matrix_world)
    bm=bmesh.new();bm.from_mesh(o.data);bm.verts.ensure_lookup_table()
    par=list(range(len(bm.verts)))
    def f(x):
        while par[x]!=x: par[x]=par[par[x]];x=par[x]
        return x
    for e in bm.edges:
        a,b=f(e.verts[0].index),f(e.verts[1].index)
        if a!=b: par[a]=b
    comp={}
    for fc in bm.faces: comp.setdefault(f(fc.verts[0].index),[]).append(fc)
    for k,fs in comp.items():
        vs={v for fc in fs for v in fc.verts}
        mn=[min(v.co[i] for v in vs)*1000 for i in range(3)];mx=[max(v.co[i] for v in vs)*1000 for i in range(3)]
        cx=(mn[0]+mx[0])/2;cy=(mn[1]+mx[1])/2;cz=(mn[2]+mx[2])/2
        # top region of the face plane: beyond the top key row (z>74) or near top edge
        if abs(cx)<16 and cz>70 and (mx[0]-mn[0])<16:
            print('C',len(fs),[round(x,1) for x in mn],[round(x,1) for x in mx],set(fc.material_index for fc in fs))
