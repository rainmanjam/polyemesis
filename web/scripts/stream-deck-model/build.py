"""Split + slim deck.glb for the web.  blender -b --python web/scripts/stream-deck-model/build.py -- in.glb faces.json out.glb [minmm]  (minmm defaults to 2.0)"""
import bpy, sys, json, math, bmesh
from mathutils import Vector, Matrix
a = sys.argv[sys.argv.index('--')+1:]
IN, FJ, OUT = a[:3]
MINMM = float(a[3]) if len(a) > 3 else 2.0
F = json.load(open(FJ))
bpy.ops.wm.read_factory_settings(use_empty=True)
bpy.ops.import_scene.gltf(filepath=IN)
for m in bpy.data.materials:
    b = m.node_tree.nodes.get('Principled BSDF') if m.use_nodes else None
    if b: print('MAT', m.name, [round(x,3) for x in b.inputs['Base Color'].default_value], 'metal', round(b.inputs['Metallic'].default_value,2), 'rough', round(b.inputs['Roughness'].default_value,2))
mm = lambda v: Vector(v) * 1000.0
KEYC = {(r, c): Vector(F[f'k{r}{c}']['c'][0]) for r in range(2) for c in range(4)}
KN = Vector(F['k00']['c'][1]).normalized()
DIALC = [Vector(F[f'dial{i}']['c'][0]) for i in range(4)]
DN = Vector(F['dial0']['c'][1]).normalized()

# ---- collect components of every mesh, in world space (metres)
groups = {}   # name -> list of (src_obj_name, [face idx])
objs = [o for o in bpy.data.objects if o.type == 'MESH']
srcs = {}
for o in objs:
    me = o.data
    me.transform(o.matrix_world); o.matrix_world = Matrix.Identity(4)
    bm = bmesh.new(); bm.from_mesh(me); bm.verts.ensure_lookup_table(); bm.faces.ensure_lookup_table()
    par = list(range(len(bm.verts)))
    def f(x):
        while par[x] != x: par[x] = par[par[x]]; x = par[x]
        return x
    for e in bm.edges:
        p, q = f(e.verts[0].index), f(e.verts[1].index)
        if p != q: par[p] = q
    comp = {}
    for fc in bm.faces: comp.setdefault(f(fc.verts[0].index), []).append(fc.index)
    keep = {}
    stats = {}
    for k, fi in comp.items():
        vs = {v for i in fi for v in bm.faces[i].verts}
        mn = Vector((min(v.co.x for v in vs), min(v.co.y for v in vs), min(v.co.z for v in vs)))
        mx = Vector((max(v.co.x for v in vs), max(v.co.y for v in vs), max(v.co.z for v in vs)))
        cen = (mn + mx) / 2; size = mx - mn
        mats = {bm.faces[i].material_index for i in fi}
        name = None
        if o.name.startswith('STREAM_DECK-PLUS_MODULE') and size.x < 0.025:
            col = min(range(4), key=lambda c: abs(KEYC[(0, c)].x - cen.x))
            row = 0 if cen.z > 0.058 else 1
            name = f'key{row}{col}'
        elif o.name.startswith('WD050-REAR') and 0 in mats and cen.y < 0.0 and cen.z < 0.02 + 0.02:
            # dial candidates: near an axis through a dial centre along DN
            for i, dc in enumerate(DIALC[:1]):
                d = cen - dc
                along = d.dot(DN); perp = (d - DN * along).length
                if perp < 0.0105 and -0.012 < along < 0.012: name = f'dial{i}'; break
        if name is None and o.name.startswith('WD050-REAR') and ((abs(cen.x) < 0.02 and 0.0535 < cen.y < 0.0605 and 0.0745 < cen.z < 0.0835 and size.x < 0.036 and size.y < 0.0075) or (0.004 < abs(cen.x) < 0.009 and 0.0505 < cen.y < 0.055 and 0.070 < cen.z < 0.075 and size.x < 0.005)):
            continue   # the modelled wordmark: removed, the face stays clean
        if name is None and o.name.startswith('WD050-REAR') and 0 in mats and cen.y < 0.0 and cen.z < 0.04 and size.x < 0.05:
            continue   # the other three dials: shipped once and instanced in the page
        if name is None:
            if max(size.x, size.y, size.z) < MINMM / 1000.0: continue
            name = 'body_' + ('stand' if 'STAND' in o.name else 'main')
        keep.setdefault(name, []).extend(fi)
    srcs[o.name] = (o, keep)
    for n, fi in keep.items(): print('GROUP', o.name[:12], n, len(fi), 'faces')
    bm.free()

def build(name, picks, origin=None):
    """new object from (src obj, face list) picks, mesh translated so `origin` is at 0."""
    bm = bmesh.new()
    mats = []
    for o, fis in picks:
        b2 = bmesh.new(); b2.from_mesh(o.data); b2.faces.ensure_lookup_table()
        fset = set(fis)
        dele = [fc for fc in b2.faces if fc.index not in fset]
        bmesh.ops.delete(b2, geom=dele, context='FACES')
        loose = [v for v in b2.verts if not v.link_faces]
        bmesh.ops.delete(b2, geom=loose, context='VERTS')
        me2 = bpy.data.meshes.new('t'); b2.to_mesh(me2); b2.free()
        # material remap into one list
        mp = []
        for m in o.data.materials:
            if m not in mats: mats.append(m)
            mp.append(mats.index(m))
        for p in me2.polygons: p.material_index = mp[p.material_index] if mp else 0
        bm.from_mesh(me2); bpy.data.meshes.remove(me2)
    me = bpy.data.meshes.new(name); bm.to_mesh(me); bm.free()
    for m in mats: me.materials.append(m)
    ob = bpy.data.objects.new(name, me); bpy.context.scene.collection.objects.link(ob)
    if origin is not None:
        me.transform(Matrix.Translation(-origin)); ob.location = origin
    return ob

names = {}
for oname, (o, keep) in srcs.items():
    for n, fi in keep.items(): names.setdefault(n, []).append((o, fi))
for n, picks in sorted(names.items()):
    origin = None
    if n.startswith('key'): origin = KEYC[(int(n[3]), int(n[4]))]
    if n.startswith('dial'): origin = DIALC[int(n[4])]
    build(n, picks, origin)
for o in objs: bpy.data.objects.remove(o, do_unlink=True)
for o in [o for o in bpy.data.objects if o.type != 'MESH']: bpy.data.objects.remove(o, do_unlink=True)
RATIO = {'body_main': 0.55, 'body_stand': 0.22}
for o in list(bpy.data.objects):
    r = RATIO.get(o.name, None)
    if r is None: continue
    bpy.context.view_layer.objects.active = o
    d = o.modifiers.new('dis', 'DECIMATE'); d.decimate_type = 'DISSOLVE'; d.angle_limit = math.radians(2.0)
    bpy.ops.object.modifier_apply(modifier='dis')
    c = o.modifiers.new('col', 'DECIMATE'); c.decimate_type = 'COLLAPSE'; c.ratio = r; c.use_collapse_triangulate = True
    bpy.ops.object.modifier_apply(modifier='col')
# dial cap: faces looking along the dial axis get their own material slot and planar UVs
dl = bpy.data.objects.get('dial0')
if dl:
    me = dl.data
    capm = me.materials[0].copy(); capm.name = 'dialcap'; me.materials.append(capm)
    ax = DN.normalized(); e1 = Vector((1, 0, 0)); e2 = ax.cross(e1).normalized()
    uvl = me.uv_layers.new(name='cap')
    top = max((v.co.dot(ax) for v in me.vertices))
    n_cap = 0
    for p in me.polygons:
        if p.normal.dot(ax) > 0.9 and all(me.vertices[i].co.dot(ax) > top - 0.0025 for i in p.vertices):
            p.material_index = len(me.materials) - 1; n_cap += 1
            for li in p.loop_indices:
                c = me.vertices[me.loops[li].vertex_index].co
                uvl.data[li].uv = (0.5 + c.dot(e1) / 0.0165, 0.5 + c.dot(e2) / 0.0165)
    print('CAPFACES', n_cap, 'top', top)
bm_ = bpy.data.objects.get('body_main')
if bm_:
    me = bm_.data
    pm = me.materials[min(1, len(me.materials)-1)].copy(); pm.name = 'plate'; me.materials.append(pm)
    ax = DN.normalized(); k = 0
    for p in me.polygons:
        if p.normal.dot(ax) > 0.985: p.material_index = len(me.materials) - 1; k += 1
    print('PLATEFACES', k, 'of', len(me.polygons))
tot = 0
for o in bpy.data.objects:
    for p in o.data.polygons: pass
    t = sum(len(p.vertices) - 2 for p in o.data.polygons); tot += t
    print('OUT', o.name, t)
print('TOTAL', tot)
bpy.ops.export_scene.gltf(filepath=OUT, export_format='GLB', export_apply=True, export_yup=True, export_texcoords=True, export_normals=True, export_cameras=False, export_lights=False)
