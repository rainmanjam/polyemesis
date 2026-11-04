import {buildRoutes,LANES,MUSIC_LANE,CLUSTER_SHIFT} from './routes';
import {readPalette} from './palette';

const root=document.querySelector('[data-signal-path]');

const diagram=document.getElementById('sp-diagram');
const demo=root.querySelector('.demo');
const wires=document.getElementById('sp-wires');
const ports=document.getElementById('sp-ports');
const srtLabel=document.getElementById('sp-srt-label');
const story=document.getElementById('sp-story');
const pauseButton=document.getElementById('sp-pause');
const replayButton=document.getElementById('sp-replay');
const motion=matchMedia('(prefers-reduced-motion: reduce)');
const canHover=matchMedia('(any-hover: hover)');
const chapters=[...root.querySelectorAll('[data-stage]')];
const nodes=['sp-source','sp-router','sp-twitch-output','sp-youtube-output','sp-archive-output'].map(id=>document.getElementById(id));
const duration=10000;
const state={elapsed:motion.matches?duration:0,includeMusic:false,reduced:motion.matches,traceProgress:-1,traceEnabled:false,flow:motion.matches?700:0,focus:null};
let scene=null,faders=null,running=false,paused=false,hasStarted=motion.matches,inView=false,frameId=0,lastTime=0,lastPaint=0,currentChapter=-1;
let traceElapsed=-1,traceRestore=null,traceComplete=false,hoverDest=null,focusDest=null,lastPreview='hover',settled=false,layout=null;
const narration=[
  ['One upload.','Video, two mics, music and game audio travel together.'],
  ['Choose what each platform hears.','Twitch gets the music. YouTube leaves it out.'],
  ['Same video. Different audio.','The archive keeps the original tracks for editing.']
];

/* Live meters in the output cards. [mic, music, game, co-host] base levels per destination, matching
   the patchbay presets: every destination carries both mics and the game bed; only YouTube drops music. */
const meterBase={twitch:[.74,.86,.66,.7],youtube:[.74,0,.66,.7],archive:[.7,.8,.68,.68]};
const meters=[...root.querySelectorAll('.destination')].map(card=>({
  dest:card.dataset.dest,bars:[...card.querySelectorAll('.meter')].map((el,i)=>({el,fill:el.firstElementChild,level:meterBase[card.dataset.dest][i],i,switched:el.id==='sp-youtube-music'}))
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
  const core=document.getElementById('sp-router-core').getBoundingClientRect();
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
    const switched=connection.index===2&&track===MUSIC_LANE,off=switched&&!state.includeMusic;
    wires.append(makeSvgElement('path',{d,class:`wire ${LANES[track]}${off?' off':''}`,'data-music-lane':switched?'1':'0'}));
  }));
  layout.connections.forEach(({start:a,end:b},i)=>{
    const shift=layout.mobile?(i===1?-CLUSTER_SHIFT:i===2?CLUSTER_SHIFT:0):0;
    for(const p of [{x:a.x+shift,y:a.y},b])ports.append(makeSvgElement('circle',{cx:p.x,cy:p.y,r:3,class:'terminal'}));
  });
  const first=layout.connections[0];
  srtLabel.style.left=`${layout.mobile?first.start.x+40:(first.start.x+first.end.x)/2}px`;
  srtLabel.style.top=`${layout.mobile?(first.start.y+first.end.y)/2+10:first.start.y-14}px`;
  scene?.resize(layout);faders?.resize(layout.core);
  if(window.__signalPath)window.__signalPath.layout=layout;
  requestPaint();
}

function armLive() {story.setAttribute('aria-live','polite');story.setAttribute('aria-atomic','true')}

function showNarration(heading,copy) {
  const strong=document.createElement('strong');strong.textContent=heading;
  story.replaceChildren(strong,document.createTextNode(` ${copy}`));
}

function syncTransport() {
  const ended=state.elapsed>=duration;
  demo.classList.toggle('is-paused',paused||(!running&&!hasStarted));
  demo.dataset.playback=paused?(ended?'complete':'paused'):ended?'complete':'playing';
  pauseButton.disabled=motion.matches;replayButton.disabled=motion.matches;
  const label=motion.matches?'Reduced motion: static diagram':paused?(ended?'Resume animation':'Play animation'):'Pause animation';
  pauseButton.setAttribute('aria-label',label);pauseButton.title=label;
  syncVideos();
}

/* The programme video is the same stream on every card. It plays only while the demo is on screen,
   the tab is visible, motion is allowed and the viewer has not paused; otherwise the poster shows. */
const videos=[...root.querySelectorAll('video.stream')];
function syncVideos() {
  const play=inView&&!document.hidden&&!motion.matches&&!paused;
  for(const video of videos){
    if(play){if(video.preload!=='auto')video.preload='auto';if(video.paused)video.play().catch(()=>{})}
    else if(!video.paused)video.pause();
  }
}

function focusFor() {
  /* Whichever of hover and focus happened last wins, so a pointer resting on one card does not freeze the preview while Tab moves through the others. */
  if(lastPreview==='focus'&&focusDest)return focusDest;
  if(hoverDest||focusDest)return hoverDest||focusDest;
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
      if(bar.switched)base=state.includeMusic?.8:0;
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
  root.querySelectorAll('.point').forEach(point=>point.classList.toggle('active',chapter===1&&!point.classList.contains('off')));
  document.getElementById('sp-progress').style.transform=`scaleX(${state.elapsed/duration})`;
  document.getElementById('sp-time').textContent=`00:${String(Math.floor(state.elapsed/1000)).padStart(2,'0')} / 00:10`;
  const arrival=traceElapsed>=0&&state.traceEnabled&&state.traceProgress>.85&&state.traceProgress<=1;
  document.getElementById('sp-youtube-output').dataset.arrived=String(arrival);
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
  else lastTime=0;
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
replayButton.addEventListener('click',()=>{
  if(motion.matches){state.elapsed=duration;currentChapter=-1;setPlayback(false)}else setPlayback(true,true);
});
chapters.forEach((button,i)=>button.addEventListener('click',()=>{
  setPlayback(false);state.elapsed=[1500,4000,8500][i];currentChapter=-1;syncTransport();requestPaint();
}));

document.getElementById('sp-music-toggle').addEventListener('change',event=>{
  /* Captured BEFORE setPlayback, which clears traceRestore: a second toggle mid-trace must keep
     restoring to the chapter the first one began in, not to the 10 s the first one jumped to. */
  const restoreElapsed=traceRestore??state.elapsed;
  state.includeMusic=event.target.checked;state.traceEnabled=state.includeMusic;
  narration[1][1]=state.includeMusic?'YouTube includes music. Each output is controlled separately.':'Twitch gets the music. YouTube leaves it out.';
  document.getElementById('sp-youtube-output').setAttribute('aria-label',state.includeMusic?'YouTube mix: both mics, music and game':'YouTube mix: both mics and game, no music');
  document.getElementById('sp-youtube-mix').textContent=state.includeMusic?'Mics + music + game':'Mics + game · no music';
  document.getElementById('sp-youtube-music').classList.toggle('silent',!state.includeMusic);
  document.getElementById('sp-music-point').classList.toggle('off',!state.includeMusic);
  wires.querySelectorAll('[data-music-lane="1"]').forEach(path=>path.classList.toggle('off',!state.includeMusic));
  setPlayback(false);state.elapsed=duration;traceRestore=motion.matches?null:restoreElapsed;currentChapter=-1;
  traceElapsed=motion.matches?-1:0;state.traceProgress=motion.matches?-1:0;
  drawFrame();
  armLive(); /* a switch toggled by assistive tech has had no pointer or key event on the demo */
  showNarration(state.includeMusic?'Music → YouTube.':'Music excluded from YouTube.',state.includeMusic?(motion.matches?'YouTube now includes the music track. The other outputs keep their mixes.':'Follow the magenta signal. The other outputs keep their mixes.'):'Twitch keeps its music. The archive keeps the original tracks.');
  lastTime=0;settled=false;requestTick();
});

/* Hover or keyboard focus on a destination shows its mix on the faders. Hover and focus are tracked
   separately so that a pointer leaving a card that still has focus does not clear its preview, and
   blur clears only the focus half. Each card is a tabindex=0 group, so Tab reaches it and a tap on
   touch focuses it too. */
root.querySelectorAll('.destination').forEach(card=>{
  const dest=card.dataset.dest;
  card.addEventListener('pointerenter',event=>{if(event.pointerType!=='touch'){hoverDest=dest;lastPreview='hover';requestPaint()}});
  card.addEventListener('pointerleave',()=>{if(hoverDest===dest){hoverDest=null;requestPaint()}});
  card.addEventListener('focusin',()=>{focusDest=dest;lastPreview='focus';requestPaint()});
  card.addEventListener('focusout',event=>{if(!card.contains(event.relatedTarget)){if(focusDest===dest)focusDest=null;requestPaint()}});
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
  if(motion.matches){
    /* Drop any switch trace in flight: its restore point would otherwise rewind the static completed frame. */
    traceElapsed=-1;traceRestore=null;traceComplete=false;state.traceProgress=-1;cancelAnimationFrame(frameId);frameId=0;lastTime=0;
    faders?.setTilt(null);paused=true;running=false;state.elapsed=duration;state.flow=700;currentChapter=-1;syncTransport();requestPaint()}
  else{syncTransport();requestTick()}
});
/* Arm the live region on first interaction so later narration is announced without chattering through autoplay. */

for(const target of [demo,root.querySelector('.steps')]){target.addEventListener('pointerdown',armLive,{once:true});target.addEventListener('keydown',armLive,{once:true})}
document.addEventListener('visibilitychange',()=>{
  cancelAnimationFrame(frameId);frameId=0;lastTime=0;
  if(!document.hidden)requestTick();
  syncVideos();
});
const observer=new IntersectionObserver(entries=>{
  inView=entries[entries.length-1].isIntersecting;
  if(inView){
    if(!hasStarted&&!motion.matches){hasStarted=true;running=true;syncTransport()}
    lastTime=0;requestTick();
  }else{cancelAnimationFrame(frameId);frameId=0;lastTime=0}
  syncVideos();
},{threshold:.12});
observer.observe(demo);
const resizeObserver=new ResizeObserver(refreshLayout);resizeObserver.observe(diagram);
document.fonts.ready.then(refreshLayout).catch(()=>{});
if(location.search.includes('debug'))window.__signalPath={state,get layout(){return layout},set layout(_){},scene:()=>scene,faders:()=>faders};

async function startLayers(){
const sceneHost=document.getElementById('sp-three-layer');
const palette=readPalette(root);
function guardContext(canvas,onLost) {
  /* No preventDefault: the context is not restored, the SVG/static fallback is permanent for this page view. */
  canvas.addEventListener('webglcontextlost',onLost);
}
try {
  const {createSignalScene}=await import('./scene.js');
  scene=createSignalScene(sceneHost,palette);
  demo.classList.add('three-ready');demo.dataset.renderer='threejs';
  guardContext(scene.canvas,()=>{demo.classList.remove('three-ready');demo.dataset.renderer='svg';scene?.dispose();scene=null;requestPaint()});
} catch(error) {
  demo.dataset.renderer='svg';
  document.getElementById('sp-render-note').textContent='Static path available. Explore each mix with the switch.';
}
try {
  const {createFaders}=await import('./faders.js');
  faders=createFaders(document.getElementById('sp-router-core'),palette);
  demo.classList.add('core-ready');
  guardContext(faders.canvas,()=>{demo.classList.remove('core-ready');faders?.dispose();faders=null;requestPaint()});
} catch(error) {
  console.info('Faders unavailable, showing the static mark.',error);
  demo.classList.remove('core-ready');
}
refreshLayout();
}

/* pagehide also fires when the page enters the back/forward cache. Then nothing is torn down (the
   canvases and observers must still be there on restore); the loop and the videos just stop, and
   pageshow starts them again. Only a real unload disposes the GL contexts. */
window.addEventListener('pagehide',event=>{
  cancelAnimationFrame(frameId);frameId=0;lastTime=0;
  for(const video of videos)video.pause();
  if(event.persisted)return;
  observer.disconnect();resizeObserver.disconnect();scene?.dispose();faders?.dispose();
});
window.addEventListener('pageshow',event=>{if(event.persisted){lastTime=0;syncVideos();requestPaint()}});


let started=false;
/** Called once by the lazy loader in SignalPath.astro, when the hero is near the viewport and the page is idle. */
export async function init(){
  if(started)return;started=true;
  root.dataset.ready='';
  syncTransport();requestPaint();
  await startLayers();
  syncTransport();requestPaint();
}
