import bpy,sys,bmesh,collections
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
    comp=collections.defaultdict(list)
    for fc in bm.faces: comp[f(fc.verts[0].index)].append(fc)
    cnt=collections.Counter()
    for k,fs in comp.items():
        n=sum(1 for fc in fs if abs(fc.calc_center_median().x)<0.02 and 0.0535<fc.calc_center_median().y<0.0605 and 0.0745<fc.calc_center_median().z<0.0835)
        if n:
            vs={v for fc in fs for v in fc.verts}
            mn=[min(v.co[i] for v in vs)*1000 for i in range(3)];mx=[max(v.co[i] for v in vs)*1000 for i in range(3)]
            print('CMP',len(fs),n,[round(x,1) for x in mn],[round(x,1) for x in mx])
