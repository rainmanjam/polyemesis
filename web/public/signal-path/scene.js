import * as THREE from './vendor/three.js';

const COLORS = [0x91abe0, 0x69cd8a, 0xe3b76f, 0x72bad3];

/** Decorative WebGL layer; all labels, controls and the static path remain in HTML/SVG. */
export function createSignalScene(host) {
  const renderer = new THREE.WebGLRenderer({alpha:true,antialias:true,powerPreference:'low-power'});
  renderer.setPixelRatio(Math.min(devicePixelRatio, 1.75));
  renderer.setClearColor(0x000000, 0);
  renderer.outputColorSpace = THREE.SRGBColorSpace;
  renderer.toneMapping = THREE.NoToneMapping;
  renderer.domElement.setAttribute('aria-hidden','true');
  host.append(renderer.domElement);
  const scene = new THREE.Scene();
  const camera = new THREE.OrthographicCamera(-1,1,1,-1,0.1,2000);
  camera.position.z = 1000;
  scene.add(new THREE.AmbientLight(0xffffff,2));
  const key = new THREE.DirectionalLight(0xdce7ff,3.5);
  key.position.set(-200,300,500);scene.add(key);
  const fill = new THREE.DirectionalLight(0x91abe0,1.4);
  fill.position.set(200,-100,300);scene.add(fill);
  const network = new THREE.Group();scene.add(network);
  const board = new THREE.Group();scene.add(board);
  const plates = [];
  const boardMaterial = new THREE.MeshStandardMaterial({color:0x2c3544,metalness:.62,roughness:.35});
  const edgeMaterial = new THREE.MeshStandardMaterial({color:0x62758e,metalness:.7,roughness:.3});
  for(let i=0;i<3;i++) {
    const plate=new THREE.Mesh(new THREE.BoxGeometry(142,8,74),i===2?edgeMaterial:boardMaterial);
    plate.position.y=(i-1)*12;board.add(plate);plates.push(plate);
  }
  const chip = new THREE.Mesh(new THREE.BoxGeometry(62,8,42),new THREE.MeshStandardMaterial({color:0x151b26,metalness:.4,roughness:.4}));
  chip.position.y=20;board.add(chip);
  const indicators=[];
  for(let i=0;i<3;i++) {
    const material=new THREE.MeshStandardMaterial({color:COLORS[i+1],emissive:COLORS[i+1],emissiveIntensity:.35,roughness:.4,metalness:.3});
    const rail=new THREE.Mesh(new THREE.BoxGeometry(108,2.5,2.2),material);
    rail.position.set(0,25,-14+i*13);board.add(rail);indicators.push(rail);
    for(let x=-54;x<=54;x+=18) {
      const pin=new THREE.Mesh(new THREE.BoxGeometry(3,3,7),edgeMaterial);
      pin.position.set(x,11,41);board.add(pin);
    }
  }
  board.rotation.set(.58,-.32,-.13);
  let width=1,height=1,routes=[],trace=null,layout=null,disposed=false;
  const packetGeometry=new THREE.BoxGeometry(4.5,3,3);
  const smallPacketGeometry=new THREE.BoxGeometry(2,1.5,1.5);
  const vector = new THREE.Vector3();

  function clearNetwork() {
    network.traverse(object=>{
      if(object.geometry&&object.geometry!==packetGeometry&&object.geometry!==smallPacketGeometry)object.geometry.dispose();
      if(object.material)object.material.dispose();
    });
    network.clear();routes=[];trace=null;
  }

  function curveFor(connection, lane=0) {
    const {start,end,vertical,via}=connection;
    const a=new THREE.Vector3(start.x-width/2+(vertical?lane:0),height/2-start.y-(vertical?0:lane),0);
    const d=new THREE.Vector3(end.x-width/2+(vertical?lane:0),height/2-end.y-(vertical?0:lane),0);
    let b,c;
    if(via) {
      b=new THREE.Vector3(via-width/2,a.y,24);
      c=new THREE.Vector3(via-width/2,d.y,24);
    } else if(vertical) {
      const dy=(d.y-a.y)*.48;
      b=new THREE.Vector3(a.x,a.y+dy,36);c=new THREE.Vector3(d.x,d.y-dy,36);
    } else {
      const dx=(d.x-a.x)*.5;
      b=new THREE.Vector3(a.x+dx,a.y,36);c=new THREE.Vector3(d.x-dx,d.y,36);
    }
    return new THREE.CubicBezierCurve3(a,b,c,d);
  }

  function makeRoute(connection,index,track) {
    const curve=curveFor(connection,(track-1.5)*5);
    const color=COLORS[track];
    const material=new THREE.MeshStandardMaterial({color,metalness:.25,roughness:.6,transparent:true,opacity:.26});
    const tube=new THREE.Mesh(new THREE.TubeGeometry(curve,40,.65,5,false),material);
    network.add(tube);
    const packets=[];
    const packetMaterial=new THREE.MeshStandardMaterial({color,emissive:color,emissiveIntensity:.45,roughness:.35,metalness:.3});
    for(let j=0;j<4;j++) {
      const mesh=new THREE.Mesh(j===0?packetGeometry:smallPacketGeometry,packetMaterial);
      mesh.visible=false;network.add(mesh);packets.push(mesh);
    }
    return {index,track,curve,tube,packets};
  }

  function resize(nextLayout) {
    if(disposed)return;
    layout=nextLayout;({width,height}=layout);
    renderer.setSize(width,height,false);
    camera.left=-width/2;camera.right=width/2;camera.top=height/2;camera.bottom=-height/2;camera.updateProjectionMatrix();
    board.position.set(layout.core.x-width/2,height/2-layout.core.y,0);
    const scale=Math.min(1.08,layout.core.width/190);board.scale.setScalar(scale);
    clearNetwork();
    layout.connections.forEach((connection,index)=>{
      for(let track=0;track<4;track++)routes.push(makeRoute(connection,index,track));
    });
    const traceConnection={start:layout.musicStart,end:layout.connections[2].end,vertical:layout.mobile};
    if(layout.mobile)traceConnection.via=layout.width-12;
    const curve=curveFor(traceConnection);
    const tube=new THREE.Mesh(new THREE.TubeGeometry(curve,48,1.3,6,false),new THREE.MeshBasicMaterial({color:COLORS[2],transparent:true,opacity:0}));
    const packet=new THREE.Mesh(new THREE.SphereGeometry(3.3,10,8),new THREE.MeshBasicMaterial({color:COLORS[2]}));
    packet.visible=false;network.add(tube,packet);trace={curve,tube,packet};
  }

  function paint(state) {
    if(disposed||!layout)return;
    const {elapsed,includeMusic,reduced,traceProgress,traceEnabled}=state;
    const stage=elapsed<2600?0:elapsed<5700?1:2;
    const movement=!reduced&&elapsed<9600;
    const boardActivity=movement&&stage===1?Math.sin((elapsed-2600)/3100*Math.PI):0;
    board.rotation.set(.58+boardActivity*.12,-.32+boardActivity*.08,-.13);
    plates.forEach((plate,i)=>plate.position.y=(i-1)*(12+boardActivity*4));
    indicators.forEach((rail,i)=>rail.material.emissiveIntensity=.3+boardActivity*(.3+i*.07));
    routes.forEach(route=>{
      const enabled=!(route.index===2&&route.track===2&&!includeMusic);
      const focus=stage===0?route.index===0:stage===1?route.index>0:route.index>0;
      route.tube.material.opacity=enabled?(focus?.52:.18):.055;
      const begins=route.index===0?180+route.track*90:3000+route.index*260+route.track*100;
      const duration=route.index===0?1900:1900;
      const head=(elapsed-begins)/duration;
      route.packets.forEach((packet,j)=>{
        const p=head-j*.055;
        packet.visible=movement&&enabled&&p>=0&&p<=1;
        if(packet.visible){route.curve.getPoint(p,vector);packet.position.copy(vector);packet.rotation.set(elapsed*.001,.4,elapsed*.0007)}
      });
    });
    const showingTrace=!reduced&&traceProgress>=0&&traceProgress<=1;
    trace.tube.material.opacity=showingTrace?.55:0;
    trace.packet.visible=showingTrace;
    if(showingTrace){trace.curve.getPoint(traceEnabled?traceProgress:Math.min(traceProgress,.045),vector);trace.packet.position.copy(vector);trace.packet.scale.setScalar(traceEnabled?1:1+Math.sin(traceProgress*Math.PI)*.5)}
    renderer.render(scene,camera);
  }

  function dispose() {
    if(disposed)return;disposed=true;
    clearNetwork();
    const geometries=new Set(),materials=new Set();
    board.traverse(object=>{if(object.geometry)geometries.add(object.geometry);if(object.material)materials.add(object.material)});
    geometries.forEach(geometry=>geometry.dispose());materials.forEach(material=>material.dispose());
    packetGeometry.dispose();smallPacketGeometry.dispose();renderer.dispose();renderer.domElement.remove();
  }
  return {resize,paint,dispose};
}
