import * as THREE from './vendor/three.js';

/*
 * The polyemesis mark (web/public/favicon.svg, 22x22 viewBox) as five 3D faders.
 * Bar geometry is taken straight from the favicon: x, width 2.5, height, fully rounded ends (rx 1.25).
 * Each bar becomes a capsule cap sliding in a dark groove. At rest the caps sit on the mark's own
 * centre line, so the silhouette reads as the logo.
 * Channels: [spare, Mic, Music, Game, Co-host], the bus order of LANES after video. The spare channel
 * holds a neutral mid position that shifts slightly with each mix. Every mix agrees with the patchbay
 * preset for that destination (MixMatrix.astro): only YouTube drops a track, the music.
 */
const BARS=[[2.5,6],[6.2,14],[9.9,9],[13.6,17],[17.3,8]];
const GROOVE=20;

const MIXES={
  rest:[.5,.5,.5,.5,.5],
  twitch:[.5,.62,.92,.68,.6],
  youtube:[.46,.62,.06,.68,.6],
  youtubeMusic:[.5,.62,.8,.68,.6],
  archive:[.5,.7,.82,.7,.7]
};

export function createFaders(host,palette){
  const COLORS=[palette.spareA,palette.mic,palette.music,palette.game,palette.cohost];
  const renderer=new THREE.WebGLRenderer({alpha:true,antialias:true,powerPreference:'low-power'});
  renderer.setPixelRatio(Math.min(devicePixelRatio||1,2));
  renderer.setClearColor(0x000000,0);
  renderer.outputColorSpace=THREE.SRGBColorSpace;
  renderer.domElement.setAttribute('aria-hidden','true');
  host.append(renderer.domElement);

  const scene=new THREE.Scene();
  const camera=new THREE.PerspectiveCamera(26,1,1,200);
  camera.position.set(0,0,60);
  scene.add(new THREE.AmbientLight(0xffffff,1.5));
  const key=new THREE.DirectionalLight(0xffffff,2.6);key.position.set(-20,28,40);scene.add(key);
  const fill=new THREE.DirectionalLight(0xaab8d8,.9);fill.position.set(24,-10,18);scene.add(fill);

  const mark=new THREE.Group();scene.add(mark);
  const grooveMaterial=new THREE.MeshStandardMaterial({color:palette.groove,roughness:.85,metalness:0});
  const faders=BARS.map(([x,h],i)=>{
    const cx=x+1.25-11;
    const groove=new THREE.Mesh(new THREE.CapsuleGeometry(1.7,GROOVE-3.4,6,16),grooveMaterial);
    groove.position.set(cx,0,-.9);mark.add(groove);
    const material=new THREE.MeshStandardMaterial({color:COLORS[i],roughness:.42,metalness:.12,emissive:COLORS[i],emissiveIntensity:.14});
    const cap=new THREE.Mesh(new THREE.CapsuleGeometry(1.25,h-2.5,6,16),material);
    cap.position.set(cx,0,0);mark.add(cap);
    return {cap,material,travel:(GROOVE-h)/2-.4,level:.5};
  });

  const baseTilt={x:-.2,y:.3};
  let tiltTarget={x:0,y:0},tilt={x:0,y:0},disposed=false,ready=false;
  const MAX_TILT=2.5*Math.PI/180;

  function resize(box){
    if(disposed)return;
    const w=Math.max(40,box.width),h=Math.max(40,box.height);
    renderer.setSize(w,h,false);
    camera.aspect=w/h;
    const fitH=26,fitW=26;
    const distance=Math.max(fitH/2/Math.tan(camera.fov*Math.PI/360),(fitW/2)/(Math.tan(camera.fov*Math.PI/360)*camera.aspect));
    camera.position.z=distance;camera.updateProjectionMatrix();ready=true;
  }

  /** nx, ny in -1..1, or null to recentre. Capped at 2.5 degrees per axis. */
  function setTilt(nx,ny){
    if(nx==null){tiltTarget={x:0,y:0};return}
    const m=Math.hypot(nx,ny),k=m>1?1/m:1;
    tiltTarget={x:ny*k*MAX_TILT,y:nx*k*MAX_TILT};
  }

  function mixFor(state){
    if(state.focus==='youtube')return state.includeMusic?MIXES.youtubeMusic:MIXES.youtube;
    return MIXES[state.focus]||MIXES.rest;
  }

  /** Returns true when nothing is still moving. */
  function paint(state,dt=0){
    if(disposed||!ready)return true;
    const mix=mixFor(state);
    const k=state.reduced?1:dt<=0?0:1-Math.exp(-dt/150);
    let settled=true;
    faders.forEach((f,i)=>{
      const idle=state.reduced?0:Math.sin(((state.flow||0)/1000)*(1.1+i*.37)+i*1.7)*.03;
      const target=mix[i]+idle;
      const previous=f.level;
      f.level=state.reduced?mix[i]:previous+(target-previous)*k;
      if(Math.abs(f.level-target)>.003)settled=false;
      f.cap.position.y=(f.level-.5)*2*f.travel;
      f.material.emissiveIntensity=.12+Math.max(0,f.level-.5)*.25;
    });
    const tk=state.reduced?1:dt<=0?0:1-Math.exp(-dt/110);
    tilt.x+=(tiltTarget.x-tilt.x)*tk;tilt.y+=(tiltTarget.y-tilt.y)*tk;
    if(Math.abs(tilt.x-tiltTarget.x)>1e-4||Math.abs(tilt.y-tiltTarget.y)>1e-4)settled=false;
    mark.rotation.set(baseTilt.x+tilt.x,baseTilt.y+tilt.y,0);
    renderer.render(scene,camera);
    return settled;
  }

  function dispose(){
    if(disposed)return;disposed=true;
    mark.traverse(o=>{o.geometry?.dispose();if(o.material&&o.material!==grooveMaterial)o.material.dispose()});
    grooveMaterial.dispose();renderer.dispose();renderer.domElement.remove();
  }
  return {resize,paint,dispose,setTilt,canvas:renderer.domElement,levels:()=>faders.map(f=>f.level)};
}
