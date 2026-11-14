import bpy,sys,bmesh
from mathutils import Vector
bpy.ops.wm.read_factory_settings(use_empty=True)
bpy.ops.import_scene.gltf(filepath=sys.argv[sys.argv.index('--')+1])
for o in list(bpy.data.objects):
    if o.type!='MESH': continue
    bm=bmesh.new(); bm.from_mesh(o.data); bm.verts.ensure_lookup_table()
    mw=o.matrix_world
    # union-find on verts by edges
    parent=list(range(len(bm.verts)))
    def f(x):
        while parent[x]!=x: parent[x]=parent[parent[x]]; x=parent[x]
        return x
    for e in bm.edges:
        a,b=f(e.verts[0].index),f(e.verts[1].index)
        if a!=b: parent[a]=b
    groups={}
    for fc in bm.faces:
        groups.setdefault(f(fc.verts[0].index),[]).append(fc)
    rows=[]
    for k,fs in groups.items():
        vs={v for fc in fs for v in fc.verts}
        co=[mw@v.co for v in vs]
        mn=[min(c[i] for c in co) for i in range(3)];mx=[max(c[i] for c in co) for i in range(3)]
        tris=sum(len(fc.verts)-2 for fc in fs)
        rows.append((tris,[round(x*1000,1) for x in mn],[round(x*1000,1) for x in mx],set(fc.material_index for fc in fs)))
    rows.sort(key=lambda r:-r[0])
    print('==',o.name,'loose parts',len(rows))
    for r in rows[:40]: print(r)
    print('small parts count', sum(1 for r in rows if r[0]<200))
