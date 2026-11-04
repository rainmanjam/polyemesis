import * as THREE from './vendor/three.js';
import {Line2,LineGeometry,LineMaterial,EffectComposer,RenderPass,UnrealBloomPass} from './vendor/three-addons.js';
import {buildRoutes,pointAtDistance} from './routes.js';

/* Identity colours: video is neutral, tracks never borrow a status colour. */
const LANE_COLORS=[0xc3cada,0x45b5d0,0xe879f9,0x9b8cff];
const WIRE_WIDTH=[2.6,2.6,2.6,2.6];
const PACKETS_PER_LANE=3,TRAIL=6,TRAIL_STEP=4.2,SPEED=150;
const MAX_INSTANCES=16*PACKETS_PER_LANE*(1+TRAIL)+16;
const HEAD=[9,4.2],TAIL=[6,3];

/**
 * Decorative WebGL layer that sits BEHIND the cards: crisp Line2 wires, instanced packets with short
 * additive trails, and a selective bloom on the packets only. Labels, controls and the static
 * fallback stay in HTML/SVG.
 */
export function createSignalScene(host){
  const renderer=new THREE.WebGLRenderer({alpha:true,antialias:true,powerPreference:'low-power'});
  const pixelRatio=Math.min(devicePixelRatio||1,2);
  renderer.setPixelRatio(pixelRatio);
  renderer.setClearColor(0x000000,0);
  renderer.outputColorSpace=THREE.SRGBColorSpace;
  renderer.toneMapping=THREE.NoToneMapping;
  renderer.domElement.setAttribute('aria-hidden','true');
  host.append(renderer.domElement);

  const scene=new THREE.Scene();
  const camera=new THREE.OrthographicCamera(-1,1,1,-1,0.1,100);
  camera.position.z=10;
  const bloomCamera=camera.clone();
  bloomCamera.layers.set(1);
  const wires=new THREE.Group();scene.add(wires);

  /* Packets: one InstancedMesh, per-instance colour carries the trail fade. */
  const packetMaterial=new THREE.MeshBasicMaterial({color:0xffffff,transparent:true,blending:THREE.AdditiveBlending,depthWrite:false,depthTest:false});
  const packets=new THREE.InstancedMesh(new THREE.PlaneGeometry(1,1),packetMaterial,MAX_INSTANCES);
  packets.layers.set(1);packets.frustumCulled=false;
  packets.instanceMatrix.setUsage(THREE.DynamicDrawUsage);
  packets.count=0;scene.add(packets);
  camera.layers.enable(1);

  /* Selective bloom: only layer 1 (packets) is rendered into the bloom chain. */
  let quadGeometry=null,composer=null,composite=null,compositeScene=null,bloomPass=null;
  try{
    composer=new EffectComposer(renderer);
    composer.renderToScreen=false;
    composer.addPass(new RenderPass(scene,bloomCamera,null,new THREE.Color(0x000000),1));
    bloomPass=new UnrealBloomPass(new THREE.Vector2(256,256),.32,.35,.02);
    composer.addPass(bloomPass);
    composite=new THREE.ShaderMaterial({
      uniforms:{tMap:{value:null},uGain:{value:1}},
      vertexShader:'varying vec2 vUv;void main(){vUv=uv;gl_Position=vec4(position.xy,0.,1.);}',
      fragmentShader:'uniform sampler2D tMap;uniform float uGain;varying vec2 vUv;void main(){vec3 c=texture2D(tMap,vUv).rgb*uGain;vec4 o=linearToOutputTexel(vec4(c,1.));gl_FragColor=vec4(o.rgb,max(o.r,max(o.g,o.b)));}',
      blending:THREE.CustomBlending,blendSrc:THREE.OneFactor,blendDst:THREE.OneFactor,transparent:true,depthTest:false,depthWrite:false
    });
    quadGeometry=new THREE.PlaneGeometry(2,2);
    const quad=new THREE.Mesh(quadGeometry,composite);
    quad.frustumCulled=false;
    compositeScene=new THREE.Scene();compositeScene.add(quad);
  }catch(error){composer=null;composite=null;compositeScene=null}

  let bloomOn=true,width=1,height=1,routes=[],layout=null,disposed=false;
  const tmp=[0,0],tan=[1,0];
  const matrix=new THREE.Matrix4(),position=new THREE.Vector3(),quaternion=new THREE.Quaternion(),scale=new THREE.Vector3(1,1,1),axis=new THREE.Vector3(0,0,1),color=new THREE.Color();
  const laneColors=LANE_COLORS.map(hex=>new THREE.Color(hex));
  const lift=LANE_COLORS.map(hex=>new THREE.Color(hex).lerp(new THREE.Color(0xffffff),.35));

  function clearWires(){
    wires.traverse(object=>{object.geometry?.dispose();object.material?.dispose()});
    wires.clear();routes=[];
  }

  function resize(nextLayout){
    if(disposed)return;
    layout=nextLayout;({width,height}=layout);
    renderer.setSize(width,height,false);
    camera.left=-width/2;camera.right=width/2;camera.top=height/2;camera.bottom=-height/2;camera.updateProjectionMatrix();
    bloomCamera.left=camera.left;bloomCamera.right=camera.right;bloomCamera.top=camera.top;bloomCamera.bottom=camera.bottom;bloomCamera.updateProjectionMatrix();
    if(composer){composer.setPixelRatio(pixelRatio*.5);composer.setSize(width,height)}
    clearWires();
    const built=layout.routes||buildRoutes(layout);
    built.forEach(connection=>{
      connection.lanes.forEach((lane,track)=>{
        const geometry=new LineGeometry();
        geometry.setPositions(lane.pts.flatMap(([x,y])=>[x-width/2,height/2-y,0]));
        const material=new LineMaterial({color:LANE_COLORS[track],linewidth:WIRE_WIDTH[track]*pixelRatio,transparent:true,opacity:.5,depthTest:false,depthWrite:false});
        material.resolution.set(width*pixelRatio,height*pixelRatio);
        const line=new Line2(geometry,material);
        line.frustumCulled=false;
        wires.add(line);
        routes.push({index:connection.index,track,lane,line,material,opacity:.5,vis:1});
      });
    });
  }

  function begins(route){return route.index===0?150+route.track*80:2900+route.index*220+route.track*90}
  const ease=(current,target,dt)=>dt>0?current+(target-current)*(1-Math.exp(-dt/120)):current;

  function place(i,x,y,angle,sx,sy,rgb,brightness){
    position.set(x-width/2,height/2-y,2);
    quaternion.setFromAxisAngle(axis,angle);
    scale.set(sx,sy,1);
    matrix.compose(position,quaternion,scale);
    packets.setMatrixAt(i,matrix);
    color.copy(rgb).multiplyScalar(brightness);
    packets.setColorAt(i,color);
  }

  function paint(state,dt=0){
    if(disposed||!layout)return true;
    const {elapsed,includeMusic,reduced,traceProgress,traceEnabled,flow}=state;
    const stage=elapsed<2600?0:elapsed<5700?1:2;
    const moving=!reduced;
    let settled=true,n=0;
    const showTrace=moving&&traceProgress>=0&&traceProgress<=1;
    routes.forEach(route=>{
      const enabled=!(route.index===2&&route.track===2&&!includeMusic);
      const started=elapsed>=begins(route);
      const focus=stage===0?route.index===0:route.index>0?true:.55;
      const traced=showTrace&&route.index===2&&route.track===2;
      const target=!started?(route.index===0?.5:.14):!enabled?(traced?.5:.1):focus===true?.92:stage===0?.3:.45;
      route.opacity=reduced?target:ease(route.opacity,target,dt);
      if(Math.abs(route.opacity-target)>.004)settled=false;
      route.material.opacity=Math.min(1,route.opacity+(traced?.25:0));
      route.material.linewidth=(route.track===2&&route.index===2&&enabled&&started&&includeMusic?3.2:WIRE_WIDTH[route.track])*pixelRatio;
      if(!started||!enabled||n+PACKETS_PER_LANE*(1+TRAIL)>MAX_INSTANCES-15)return;
      const length=route.lane.length,period=Math.max(length+40,150),flowPx=(flow||0)*.001*SPEED;
      const bright=route.index===0||focus===true?1:.6;
      for(let j=0;j<PACKETS_PER_LANE;j++){
        const head=(flowPx+j*period/PACKETS_PER_LANE+route.track*23+route.index*41)%period;
        for(let t=0;t<=TRAIL;t++){
          const d=head-t*TRAIL_STEP;
          if(d<2||d>length-2)continue;
          pointAtDistance(route.lane,d,tmp,tan);
          const fade=t===0?1:Math.pow(1-t/(TRAIL+1),1.7)*.62;
          const size=t===0?HEAD:TAIL;
          place(n++,tmp[0],tmp[1],Math.atan2(-tan[1],tan[0]),size[0]*(t===0?1:1-t*.07),size[1]*(t===0?1:1-t*.09),t===0?lift[route.track]:laneColors[route.track],fade*bright*(t===0?1.15:1));
        }
      }
    });
    if(showTrace){
      const route=routes.find(r=>r.index===2&&r.track===2);
      if(route){
        const d=(traceEnabled?traceProgress:Math.min(traceProgress,.14))*route.lane.length;
        for(let t=0;t<=12;t++){
          const dd=d-t*3.6;
          if(dd<1)continue;
          pointAtDistance(route.lane,dd,tmp,tan);
          const fade=t===0?1:Math.pow(1-t/13,1.5)*.8;
          const fadeOut=traceEnabled?1:Math.max(0,1-Math.max(0,traceProgress-.08)*14);
          place(n++,tmp[0],tmp[1],Math.atan2(-tan[1],tan[0]),(t===0?14:10)*(1-t*.04),(t===0?6.4:4.4)*(1-t*.05),t===0?lift[2]:laneColors[2],fade*fadeOut*1.25);
        }
      }
    }
    packets.count=n;
    packets.instanceMatrix.needsUpdate=true;
    if(packets.instanceColor)packets.instanceColor.needsUpdate=true;

    renderer.autoClear=true;
    renderer.render(scene,camera);
    if(composer&&bloomOn&&n>0){
      composer.render(dt/1000);
      renderer.setRenderTarget(null);
      composite.uniforms.tMap.value=composer.readBuffer.texture;
      renderer.autoClear=false;
      renderer.render(compositeScene,camera);
      renderer.autoClear=true;
    }
    return settled;
  }

  function dispose(){
    if(disposed)return;disposed=true;
    clearWires();
    packets.geometry.dispose();packetMaterial.dispose();packets.dispose();
    composer?.dispose();composite?.dispose();quadGeometry?.dispose();bloomPass?.dispose?.();
    renderer.dispose();renderer.domElement.remove();
  }
  return {resize,paint,dispose,canvas:renderer.domElement,get bloom(){return !!composer},setBloom(on){bloomOn=!!on}};
}
