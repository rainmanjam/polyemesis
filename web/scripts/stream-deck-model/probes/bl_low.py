import bpy,sys,collections
bpy.ops.wm.read_factory_settings(use_empty=True)
bpy.ops.import_scene.gltf(filepath=sys.argv[sys.argv.index('--')+1])
pts=[]
for o in bpy.data.objects:
    if o.type!='MESH': continue
    mw=o.matrix_world
    for v in o.data.vertices:
        w=mw@v.co; pts.append((w.z*1000,w.x*1000,w.y*1000,o.name[:10]))
pts.sort()
print('MINZ',pts[0])
for lim in (0.5,3):
    low=[p for p in pts if p[0]<pts[0][0]+lim]
    c=collections.Counter((round(p[1]/10)*10,round(p[2]/10)*10,p[3]) for p in low)
    print('LOW',lim,len(low),sorted(c.items())[:50])
