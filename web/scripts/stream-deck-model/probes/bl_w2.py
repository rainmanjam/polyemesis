import bpy,sys
from mathutils import Vector
bpy.ops.wm.read_factory_settings(use_empty=True)
bpy.ops.import_scene.gltf(filepath=sys.argv[sys.argv.index('--')+1])
for o in bpy.data.objects:
    if o.type!='MESH': continue
    me=o.data; mw=o.matrix_world; k=0; mats=set()
    for p in me.polygons:
        c=mw@p.center
        if abs(c.x)<0.02 and 0.0535<c.y<0.0605 and 0.0745<c.z<0.0835: k+=1; mats.add(me.materials[p.material_index].name if me.materials and p.material_index<len(me.materials) else None)
    print('W2',o.name,k,mats)
