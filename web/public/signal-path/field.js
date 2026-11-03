import * as THREE from './vendor/three-field.js';
const COLORS=[0x69cd8a,0xe3b76f,0x72bad3];
const SEGMENTS=100;

/** A continuous field of audio stems: the route masks also drive the audible mixes. */
export function createSignalField(host,bank) {
  const renderer=new THREE.WebGLRenderer({alpha:true,antialias:true,powerPreference:'low-power'});
  renderer.setPixelRatio(Math.min(devicePixelRatio,1.6));renderer.setClearColor(0,0);
  renderer.outputColorSpace=THREE.SRGBColorSpace;
  host.append(renderer.domElement);renderer.domElement.setAttribute('aria-hidden','true');
  const scene=new THREE.Scene(),camera=new THREE.OrthographicCamera(-1,1,1,-1,.1,2000);camera.position.z=1000;
  scene.add(new THREE.AmbientLight(0xffffff,2));const light=new THREE.DirectionalLight(0xc5d8ff,2.6);light.position.set(0,400,700);scene.add(light);
  let layout=null,routes=[],last=0,disposed=false;
  const dotGeometry=new THREE.SphereGeometry(2.4,8,6);
  const temp=new THREE.Vector3();

  function clear() {
    routes.forEach(route=>{scene.remove(route.mesh);route.mesh.geometry.dispose();route.mesh.material.dispose();route.dots.forEach(dot=>scene.remove(dot));route.dotMaterial.dispose()});routes=[];
  }
  function curve(a,b,track,output) {
    const offset=(track-1)*10;
    const p0=new THREE.Vector3(a.x-layout.width/2,layout.height/2-a.y,0);
    const p3=new THREE.Vector3(b.x-layout.width/2,layout.height/2-b.y-offset,0);
    let p1,p2;
    if(layout.mobile&&output>=0){
      const laneX=output===0?-layout.width/2+3:9;
      p1=new THREE.Vector3(laneX,p0.y-45,42+track*5);p2=new THREE.Vector3(laneX,p3.y+45,42+track*5);
    }else if(layout.mobile){const dy=(p3.y-p0.y)*.55;p1=new THREE.Vector3(p0.x+offset*1.4,p0.y+dy,42+track*5);p2=new THREE.Vector3(p3.x-offset,p3.y-dy,42+track*5)}
    else {const dx=(p3.x-p0.x)*.48;p1=new THREE.Vector3(p0.x+dx,p0.y+(output<0?-15:0),52+track*12);p2=new THREE.Vector3(p3.x-dx,p3.y,52+track*12)}
    return new THREE.CubicBezierCurve3(p0,p1,p2,p3);
  }
  function addRoute(a,b,track,output=-1) {
    const spline=curve(a,b,track,output);
    const positions=new Float32Array((SEGMENTS+1)*6),indices=[];
    for(let i=0;i<SEGMENTS;i++){const n=i*2;indices.push(n,n+1,n+2,n+1,n+3,n+2)}
    const geometry=new THREE.BufferGeometry();geometry.setAttribute('position',new THREE.Float32BufferAttribute(positions,3));geometry.setIndex(indices);
    const material=new THREE.MeshStandardMaterial({color:COLORS[track],emissive:COLORS[track],emissiveIntensity:.2,metalness:.5,roughness:.45,side:THREE.DoubleSide,transparent:true,opacity:.75});
    const mesh=new THREE.Mesh(geometry,material);mesh.frustumCulled=false;scene.add(mesh);
    const dots=[],dotMaterial=new THREE.MeshBasicMaterial({color:COLORS[track],transparent:true,opacity:.85});
    for(let i=0;i<3;i++){const dot=new THREE.Mesh(dotGeometry,dotMaterial);scene.add(dot);dots.push(dot)}
    routes.push({spline,mesh,positions,track,output,gain:1,dots,dotMaterial});
  }
  function resize(next) {
    if(disposed)return;layout=next;renderer.setSize(layout.width,layout.height,false);
    camera.left=-layout.width/2;camera.right=layout.width/2;camera.top=layout.height/2;camera.bottom=-layout.height/2;camera.updateProjectionMatrix();clear();
    layout.inputs.forEach((input,i)=>addRoute(input,{x:layout.hub.x-5,y:layout.hub.y+(i-1)*10},i));
    layout.outputs.forEach((output,j)=>{for(let track=0;track<3;track++)addRoute({x:layout.hub.x+5,y:layout.hub.y+(track-1)*10},j===2&&layout.archive?layout.archive[track]:output,track,j)});
  }
  function paint(state) {
    if(disposed||!layout)return;
    const dt=Math.min(.1,Math.max(.016,state.time-last));last=state.time;
    routes.forEach(route=>{
      const mask=route.output<0?[1,1,1]:bank.masks({...state,solo:-1},route.output);
      const target=mask[route.track];
      route.gain=state.reduced?target:route.gain+(target-route.gain)*(1-Math.exp(-dt*9));
      const selected=!state.sound||route.output<0||state.listen===route.output;
      route.mesh.material.opacity=(selected?.88:.4)*route.gain;
      const position=route.mesh.geometry.attributes.position;
      const retained=state.mode==='record'&&route.output===2;
      for(let i=0;i<=SEGMENTS;i++){
        const u=i/SEGMENTS;
        route.spline.getPoint(u,temp);
        const envelope=Math.abs(bank.sample(route.track,state.time-u*.6))*1.8;
        const taper=Math.sin(Math.PI*u);
        const wave=Math.sin(u*Math.PI*15-state.time*4+route.track)*(.7+envelope)*taper;
        const amplitude=layout.mobile?8:14;
        const width=(route.output<0?1.5:1.75)*Math.max(.01,route.gain);
        const separate=retained?(route.track-1)*12:0;
        const x=temp.x+(layout.mobile?wave*amplitude:0),y=temp.y+(!layout.mobile?wave*amplitude:0)+separate,z=temp.z+Math.sin(u*8+state.time*.8)*taper*8;
        const n=i*6;
        position.array[n]=x-(layout.mobile?width:0);position.array[n+1]=y-(layout.mobile?0:width);position.array[n+2]=z;
        position.array[n+3]=x+(layout.mobile?width:0);position.array[n+4]=y+(layout.mobile?0:width);position.array[n+5]=z+2;
      }
      position.needsUpdate=true;route.mesh.geometry.computeVertexNormals();
      route.dots.forEach((dot,i)=>{
        const u=((state.time*.24+i/3+route.track*.08)%1+1)%1;route.spline.getPoint(u,temp);dot.position.copy(temp);dot.visible=route.gain>.08;dot.scale.setScalar(route.gain*(selected?1:.65));
      });
      route.dotMaterial.opacity=route.gain*(selected?.9:.35);
    });
    renderer.render(scene,camera);
  }
  function dispose(){if(disposed)return;disposed=true;clear();dotGeometry.dispose();renderer.dispose();renderer.domElement.remove()}
  return {resize,paint,dispose};
}
