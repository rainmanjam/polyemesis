import bpy,sys,collections
from mathutils import Vector
bpy.ops.wm.read_factory_settings(use_empty=True)
bpy.ops.import_scene.gltf(filepath=sys.argv[sys.argv.index('--')+1])
n=Vector((0,-0.762,0.647)).normalized()
for o in bpy.data.objects:
    if o.type!='MESH' or not o.name.startswith('WD050-REAR'): continue
    me=o.data; me.transform(o.matrix_world)
    lv=collections.Counter(); logo=[]
    for p in me.polygons:
        c=p.center
        if p.normal.dot(n)>0.985:
            d=round(c.dot(n)*1e4)/10  # 0.1mm bins in mm
            if abs(c.x)<0.075: lv[d]+=p.area
            if abs(c.x)<0.018 and 0.0545<c.y<0.060 and 0.0755<c.z<0.0825: logo.append(c.dot(n)*1000)
    print('LEVELS',sorted(lv.items(),key=lambda t:-t[1])[:8])
    if logo: print('LOGO',len(logo),min(logo),max(logo))
