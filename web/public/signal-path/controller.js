import {buildRoutes,LANES} from './routes.js';

const diagram=document.getElementById('diagram');
const demo=document.querySelector('.demo');
const wires=document.getElementById('wires');
const ports=document.getElementById('ports');
const srtLabel=document.getElementById('srt-label');
const story=document.getElementById('story');
const pauseButton=document.getElementById('pause');
const motion=matchMedia('(prefers-reduced-motion: reduce)');
const canHover=matchMedia('(any-hover: hover)');
const chapters=[...document.querySelectorAll('[data-stage]')];
const nodes=['source','router','twitch-output','youtube-output','archive-output'].map(id=>document.getElementById(id));
const duration=10000;
const state={elapsed:motion.matches?duration:0,includeMusic:false,reduced:motion.matches,traceProgress:-1,traceEnabled:false,flow:motion.matches?700:0,focus:null};
let scene=null,faders=null,running=false,paused=false,hasStarted=motion.matches,inView=false,frameId=0,lastTime=0,lastPaint=0,currentChapter=-1;
let traceElapsed=-1,traceRestore=null,traceComplete=false,hoverDest=null,settled=false,layout=null;
const narration=[
  ['One upload.','Video, microphone, music and game audio travel together.'],
  ['Choose what each platform hears.','Twitch gets the music. YouTube leaves it out.'],
  ['Same video. Different audio.','The archive keeps the original tracks for editing.']
];

/* Live meters in the output cards. [mic, music, game] base levels per destination. */
const meterBase={twitch:[.74,.86,.66],youtube:[.74,0,.66],archive:[.7,.8,.68]};
const meters=[...document.querySelectorAll('.destination')].map(card=>({
  dest:card.dataset.dest,bars:[...card.querySelectorAll('.meter')].map((el,i)=>({el,fill:el.firstElementChild,level:meterBase[card.dataset.dest][i],i}))
}));

function makeSvgElement(tag,attributes) {
  const element=document.createElementNS('http://www.w3.org/2000/svg',tag);
  for(const [key,value] of Object.entries(attributes))element.setAttribute(key,value);
  return element;
}

function measureConnections() {
  const rect=diagram.getBoundingClientRect();
  const localBounds=element=>{
    const r=element.getBoundingClientRect();return {x:r.left-rect.left,y:r.top-rect.top,width:r.width,height:r.height};
  };
  const bounds=nodes.map(localBounds);
  const mobile=matchMedia('(max-width:960px)').matches;
  const connections=[[0,1],[1,2],[1,3],[1,4]].map(([from,to])=>{
    const a=bounds[from],b=bounds[to];
    if(mobile)return {start:{x:a.x+a.width/2,y:a.y+a.height},end:{x:b.x+b.width/2,y:b.y},vertical:true};
    return {start:{x:a.x+a.width,y:a.y+a.height/2},end:{x:b.x,y:b.y+b.height/2},vertical:false};
  });
  const core=document.getElementById('router-core').getBoundingClientRect();
  return {width:rect.width,height:rect.height,mobile,connections,bounds,core:{width:core.width,height:core.height}};
}

function refreshLayout() {
  layout=measureConnections();
  layout.routes=buildRoutes(layout);
  wires.setAttribute('viewBox',`0 0 ${layout.width} ${layout.height}`);wires.replaceChildren();
  ports.setAttribute('viewBox',`0 0 ${layout.width} ${layout.height}`);ports.replaceChildren();
  /* SVG fallback wires: every lane in its identity colour, drawn behind the cards. */
  layout.routes.forEach(connection=>connection.lanes.forEach((lane,track)=>{
    const d='M'+lane.pts.map(([x,y])=>`${x.toFixed(1)} ${y.toFixed(1)}`).join(' L');
    const off=connection.index===2&&track===2&&!state.includeMusic;
    wires.append(makeSvgElement('path',{d,class:`wire ${LANES[track]}${off?' off':''}`,'data-music-lane':connection.index===2&&track===2?'1':'0'}));
  }));
  layout.connections.forEach(({start:a,end:b},i)=>{
    const shift=layout.mobile?(i===1?-20:i===2?20:0):0;
    for(const p of [{x:a.x+shift,y:a.y},b])ports.append(makeSvgElement('circle',{cx:p.x,cy:p.y,r:3,class:'terminal'}));
  });
  const first=layout.connections[0];
  srtLabel.style.left=`${layout.mobile?first.start.x+40:(first.start.x+first.end.x)/2}px`;
  srtLabel.style.top=`${layout.mobile?(first.start.y+first.end.y)/2+10:first.start.y-14}px`;
  scene?.resize(layout);faders?.resize(layout.core);
  if(window.__signalPath)window.__signalPath.layout=layout;
  requestPaint();
}

function showNarration(heading,copy) {
  const strong=document.createElement('strong');strong.textContent=heading;
  story.replaceChildren(strong,document.createTextNode(` ${copy}`));
}

function syncTransport() {
  const ended=state.elapsed>=duration;
  demo.classList.toggle('is-paused',paused||(!running&&!hasStarted));
  demo.dataset.playback=paused?(ended?'complete':'paused'):ended?'complete':'playing';
  pauseButton.disabled=motion.matches;
  const label=motion.matches?'Reduced motion: static diagram':paused?(ended?'Resume animation':'Play animation'):'Pause animation';
  pauseButton.setAttribute('aria-label',label);pauseButton.title=label;
}

function focusFor() {
  if(hoverDest)return hoverDest;
  if(traceElapsed>=0)return 'youtube';
  const stage=state.elapsed<2600?0:state.elapsed<5700?1:2;
  if(stage===0)return null;
  if(stage===1)return state.elapsed<4150?'twitch':'youtube';
  const t=state.elapsed>=duration?Math.floor(state.flow/1800):Math.floor((state.elapsed-5700)/1400);
  return t%2===0?'twitch':'youtube';
}

function updateMeters(dt) {
  let moving=false;
  const t=state.flow/1000;
  for(const card of meters){
    for(const bar of card.bars){
      let base=meterBase[card.dest][bar.i];
      if(card.dest==='youtube'&&bar.i===1)base=state.includeMusic?.8:0;
      const wobble=state.reduced||base===0?1:.86+.14*Math.sin(t*(1.7+bar.i*.8)+bar.i*2.1)*Math.sin(t*(.9+bar.i*.3)+1.3)+.05*Math.sin(t*(5+bar.i));
      const target=base*wobble;
      const k=state.reduced?1:dt<=0?0:1-Math.exp(-dt/(base===0||target<bar.level-.2?220:90));
      bar.level=bar.level+(target-bar.level)*k;
      if(Math.abs(bar.level-target)>.004)moving=true;
      bar.fill.style.transform=`scaleX(${Math.max(0,bar.level).toFixed(3)})`;
    }
  }
  return moving;
}

function drawFrame(dt=0) {
  const chapter=state.elapsed<2600?0:state.elapsed<5700?1:2;
  if(chapter!==currentChapter){
    currentChapter=chapter;
    if(traceElapsed<0)showNarration(...narration[chapter]);
    chapters.forEach((button,i)=>{if(chapter===i)button.setAttribute('aria-current','step');else button.removeAttribute('aria-current')});
  }
  nodes.forEach((node,i)=>node.dataset.active=String(chapter===0?i===0:chapter===1?i===1:i>1));
  document.querySelectorAll('.point').forEach(point=>point.classList.toggle('active',chapter===1&&!point.classList.contains('off')));
  document.getElementById('progress').style.transform=`scaleX(${state.elapsed/duration})`;
  document.getElementById('time').textContent=`00:${String(Math.floor(state.elapsed/1000)).padStart(2,'0')} / 00:10`;
  const arrival=traceElapsed>=0&&state.traceEnabled&&state.traceProgress>.85&&state.traceProgress<=1;
  document.getElementById('youtube-output').dataset.arrived=String(arrival);
  state.focus=focusFor();
  const metersMoving=updateMeters(dt);
  const sceneSettled=scene?scene.paint(state,dt):true;
  const fadersSettled=faders?faders.paint(state,dt):true;
  settled=sceneSettled&&fadersSettled&&!metersMoving;
}

/** Draw once now and keep ticking until tweens settle. */
function requestPaint() {drawFrame(0);settled=false;requestTick()}

function ambient() {return !paused&&!motion.matches&&hasStarted}

function requestTick() {
  if(frameId||document.hidden||!inView)return;
  if(running||traceElapsed>=0||ambient()||!settled)frameId=requestAnimationFrame(tick);
}

function tick(now) {
  frameId=0;
  const rawDelta=lastTime?Math.min(now-lastTime,80):0;
  const complete=state.elapsed>=duration&&traceElapsed<0&&!running;
  if(complete&&now-lastPaint<30){requestTick();return}
  lastTime=now;lastPaint=now;
  const delta=rawDelta;
  if(ambient())state.flow+=delta;
  if(traceElapsed>=0){
    traceElapsed+=delta;state.traceProgress=traceElapsed/1700;
    if(state.traceProgress>1){
      traceElapsed=-1;state.traceProgress=-1;traceComplete=true;
      if(traceRestore!==null){state.elapsed=traceRestore;traceRestore=null;currentChapter=-1}
    }
  } else if(running){
    state.elapsed=Math.min(duration,state.elapsed+delta);
    if(state.elapsed>=duration){running=false;syncTransport()}
  }
  drawFrame(delta);
  if(traceComplete){traceComplete=false;showNarration(state.includeMusic?'Music reaches YouTube.':'Music stays out of YouTube.',state.includeMusic?'Twitch and the archive keep their own settings.':'Twitch keeps the full mix. The archive keeps the separate tracks.')}
  requestTick();
}

function setPlayback(next,rewind=false) {
  hasStarted=true;
  if(rewind||next&&state.elapsed>=duration){state.elapsed=0;currentChapter=-1}
  traceElapsed=-1;state.traceProgress=-1;traceRestore=null;
  paused=!next||motion.matches;
  running=next&&!motion.matches&&state.elapsed<duration;lastTime=0;
  if(!next){cancelAnimationFrame(frameId);frameId=0}
  syncTransport();requestPaint();
}

pauseButton.addEventListener('click',()=>{
  if(paused){
    if(state.elapsed>=duration){hasStarted=true;paused=false;running=false;lastTime=0;syncTransport();requestPaint()}
    else setPlayback(true);
  }else setPlayback(false);
});
document.getElementById('replay').addEventListener('click',()=>{
  if(motion.matches){state.elapsed=duration;currentChapter=-1;setPlayback(false)}else setPlayback(true,true);
});
chapters.forEach((button,i)=>button.addEventListener('click',()=>{
  setPlayback(false);state.elapsed=[1500,4000,8500][i];currentChapter=-1;requestPaint();
}));

document.getElementById('music-toggle').addEventListener('change',event=>{
  const previousElapsed=state.elapsed;
  state.includeMusic=event.target.checked;state.traceEnabled=state.includeMusic;
  narration[1][1]=state.includeMusic?'YouTube includes music. Each output is controlled separately.':'Twitch gets the music. YouTube leaves it out.';
  document.getElementById('youtube-mix').textContent=state.includeMusic?'Mic + music + game':'Mic + game · no music';
  document.getElementById('youtube-music').classList.toggle('silent',!state.includeMusic);
  document.getElementById('music-point').classList.toggle('off',!state.includeMusic);
  wires.querySelectorAll('[data-music-lane="1"]').forEach(path=>path.classList.toggle('off',!state.includeMusic));
  setPlayback(false);state.elapsed=duration;traceRestore=motion.matches?null:(traceRestore??previousElapsed);currentChapter=-1;
  traceElapsed=motion.matches?-1:0;state.traceProgress=motion.matches?-1:0;
  drawFrame();
  showNarration(state.includeMusic?'Music → YouTube.':'Music excluded from YouTube.',state.includeMusic?(motion.matches?'YouTube now includes the music track. The other outputs keep their mixes.':'Follow the magenta signal. The other outputs keep their mixes.'):'Twitch keeps its music. The archive keeps the original tracks.');
  story.setAttribute('aria-live','polite');
  lastTime=0;settled=false;requestTick();
});

/* Hover or focus on a destination shows its mix on the faders. */
document.querySelectorAll('.destination').forEach(card=>{
  const set=value=>{hoverDest=value;requestPaint()};
  card.addEventListener('pointerenter',event=>{if(event.pointerType!=='touch')set(card.dataset.dest)});
  card.addEventListener('pointerleave',()=>set(null));
  card.addEventListener('focusin',()=>set(card.dataset.dest));
  card.addEventListener('focusout',event=>{if(!card.contains(event.relatedTarget))set(null)});
});

/* Pointer tilt: 2.5 degrees max, faders only, off for reduced motion and touch-only devices. */
function tiltEnabled() {return !motion.matches&&canHover.matches}
diagram.addEventListener('pointermove',event=>{
  if(!tiltEnabled()||event.pointerType==='touch'||!faders)return;
  const r=diagram.getBoundingClientRect();
  faders.setTilt(Math.max(-1,Math.min(1,((event.clientX-r.left)/r.width-.5)*2)),Math.max(-1,Math.min(1,((event.clientY-r.top)/r.height-.5)*2)));
  settled=false;requestTick();
});
diagram.addEventListener('pointerleave',()=>{faders?.setTilt(null);settled=false;requestTick()});

motion.addEventListener('change',()=>{
  state.reduced=motion.matches;
  if(motion.matches){faders?.setTilt(null);paused=true;running=false;state.elapsed=duration;state.flow=700;currentChapter=-1;syncTransport();requestPaint()}
  else{syncTransport();requestTick()}
});
/* Arm the live region on first interaction so later narration is announced without chattering through autoplay. */
const armLive=()=>{story.setAttribute('aria-live','polite');story.setAttribute('aria-atomic','true')};
for(const target of [demo,document.querySelector('.steps')]){target.addEventListener('pointerdown',armLive,{once:true});target.addEventListener('keydown',armLive,{once:true})}
document.addEventListener('visibilitychange',()=>{
  cancelAnimationFrame(frameId);frameId=0;lastTime=0;
  if(!document.hidden)requestTick();
});
const observer=new IntersectionObserver(entries=>{
  inView=entries[0].isIntersecting;
  if(inView){
    if(!hasStarted&&!motion.matches){hasStarted=true;running=true;syncTransport()}
    lastTime=0;requestTick();
  }else{cancelAnimationFrame(frameId);frameId=0;lastTime=0}
},{threshold:.12});
observer.observe(demo);
const resizeObserver=new ResizeObserver(refreshLayout);resizeObserver.observe(diagram);
document.fonts.ready.then(refreshLayout);
if(location.search.includes('debug'))window.__signalPath={state,get layout(){return layout},set layout(v){},scene:()=>scene,faders:()=>faders};

const sceneHost=document.getElementById('three-layer');
function guardContext(canvas,onLost) {
  /* No preventDefault: the context is not restored, the SVG/static fallback is permanent for this page view. */
  canvas.addEventListener('webglcontextlost',onLost);
}
try {
  const {createSignalScene}=await import('./scene.js');
  scene=createSignalScene(sceneHost);
  demo.classList.add('three-ready');demo.dataset.renderer='threejs';
  guardContext(scene.canvas,()=>{demo.classList.remove('three-ready');demo.dataset.renderer='svg';scene?.dispose();scene=null;requestPaint()});
} catch(error) {
  demo.dataset.renderer='svg';
  document.getElementById('render-note').textContent='Static path available. Explore each mix with the switch.';
}
try {
  const {createFaders}=await import('./faders.js');
  faders=createFaders(document.getElementById('router-core'));
  demo.classList.add('core-ready');
  guardContext(faders.canvas,()=>{demo.classList.remove('core-ready');faders?.dispose();faders=null});
} catch(error) {
  console.info('Faders unavailable, showing the static mark.',error);
  demo.classList.remove('core-ready');
}
refreshLayout();
window.addEventListener('pagehide',()=>{cancelAnimationFrame(frameId);observer.disconnect();resizeObserver.disconnect();scene?.dispose();faders?.dispose()},{once:true});
syncTransport();requestPaint();
