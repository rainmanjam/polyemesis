const diagram=document.getElementById('diagram');
const demo=document.querySelector('.demo');
const wires=document.getElementById('wires');
const story=document.getElementById('story');
const pauseButton=document.getElementById('pause');
const motion=matchMedia('(prefers-reduced-motion: reduce)');
const chapters=[...document.querySelectorAll('[data-stage]')];
const nodes=['source','router','twitch-output','youtube-output','archive-output'].map(id=>document.getElementById(id));
const duration=10000;
const state={elapsed:motion.matches?duration:0,includeMusic:false,reduced:motion.matches,traceProgress:-1,traceEnabled:false};
let scene=null,running=false,hasStarted=motion.matches,inView=false,frameId=0,lastTime=0,currentChapter=-1;
let traceElapsed=-1,traceClock=0,traceRestore=null,traceComplete=false;
const narration=[
  ['One upload.','Video, microphone, music and game audio travel together.'],
  ['Choose what each platform hears.','Twitch gets the music. YouTube leaves it out.'],
  ['Same video. Different audio.','The archive keeps the original tracks for editing.']
];

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
  const connections=[[0,1],[1,2],[1,3],[1,4]].map(([from,to],index)=>{
    const a=bounds[from],b=bounds[to];
    if(mobile){
      if(index===3)return {start:{x:a.x+a.width,y:a.y+a.height/2},end:{x:b.x+b.width/2,y:b.y},via:rect.width-12,vertical:true};
      return {start:{x:a.x+a.width/2,y:a.y+a.height},end:{x:b.x+b.width/2,y:b.y},vertical:true};
    }
    return {start:{x:a.x+a.width,y:a.y+a.height/2},end:{x:b.x,y:b.y+b.height/2},vertical:false};
  });
  const core=localBounds(document.getElementById('router-core'));
  const music=localBounds(document.getElementById('music-point'));
  return {width:rect.width,height:rect.height,mobile,connections,core:{x:core.x+core.width/2,y:core.y+core.height/2,width:core.width},musicStart:{x:music.x+music.width/2,y:music.y+music.height/2}};
}

function refreshLayout() {
  const layout=measureConnections();
  wires.setAttribute('viewBox',`0 0 ${layout.width} ${layout.height}`);wires.replaceChildren();
  layout.connections.forEach((connection,i)=>{
    const {start:a,end:b,vertical,via}=connection;
    const controls=via?`${via} ${a.y} ${via} ${b.y}`:vertical?`${a.x} ${a.y+(b.y-a.y)*.5} ${b.x} ${b.y-(b.y-a.y)*.5}`:`${a.x+(b.x-a.x)*.5} ${a.y} ${b.x-(b.x-a.x)*.5} ${b.y}`;
    wires.append(makeSvgElement('path',{d:`M ${a.x} ${a.y} C ${controls} ${b.x} ${b.y}`,class:'wire'}));
    for(const p of [a,b])wires.append(makeSvgElement('circle',{cx:p.x,cy:p.y,r:2.5,class:'terminal'}));
    if(i===0){const text=makeSvgElement('text',{x:vertical?a.x+35:(a.x+b.x)/2,y:vertical?(a.y+b.y)/2+4:a.y-21,class:'connector-label'});text.textContent='SRT';wires.append(text)}
  });
  scene?.resize(layout);drawFrame();
}

function showNarration(heading,copy) {
  const strong=document.createElement('strong');strong.textContent=heading;
  story.replaceChildren(strong,document.createTextNode(` ${copy}`));
}

function syncTransport() {
  demo.classList.toggle('is-paused',!running);
  demo.dataset.playback=running?'playing':state.elapsed>=duration?'complete':'paused';
  pauseButton.disabled=motion.matches;
  const label=motion.matches?'Reduced motion: static diagram':running?'Pause animation':state.elapsed>=duration?'Play animation again':'Play animation';
  pauseButton.setAttribute('aria-label',label);pauseButton.title=label;
}

function drawFrame() {
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
  scene?.paint(state);
}

function requestTick() {
  if(frameId||document.hidden||!inView)return;
  if(running||traceElapsed>=0)frameId=requestAnimationFrame(tick);
}

function tick(now) {
  frameId=0;
  const delta=lastTime?Math.min(now-lastTime,80):0;lastTime=now;
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
  drawFrame();
  if(traceComplete){traceComplete=false;showNarration(state.includeMusic?'Music reaches YouTube.':'Music stays out of YouTube.',state.includeMusic?'Twitch and the archive keep their own settings.':'Twitch keeps the full mix. The archive keeps the separate tracks.')}
  requestTick();
}

function setPlayback(next,rewind=false) {
  hasStarted=true;
  if(rewind||next&&state.elapsed>=duration){state.elapsed=0;currentChapter=-1}
  traceElapsed=-1;state.traceProgress=-1;traceRestore=null;
  running=next&&!motion.matches;lastTime=0;
  if(!running){cancelAnimationFrame(frameId);frameId=0}
  syncTransport();drawFrame();requestTick();
}

pauseButton.addEventListener('click',()=>setPlayback(!running));
document.getElementById('replay').addEventListener('click',()=>{
  if(motion.matches){state.elapsed=duration;currentChapter=-1;setPlayback(false)}else setPlayback(true,true);
});
chapters.forEach((button,i)=>button.addEventListener('click',()=>{
  setPlayback(false);state.elapsed=[1500,4000,8500][i];currentChapter=-1;drawFrame();
}));

document.getElementById('music-toggle').addEventListener('change',event=>{
  const previousElapsed=state.elapsed;
  state.includeMusic=event.target.checked;state.traceEnabled=state.includeMusic;
  narration[1][1]=state.includeMusic?'YouTube includes music. Each output is controlled separately.':'Twitch gets the music. YouTube leaves it out.';
  document.getElementById('youtube-mix').textContent=state.includeMusic?'Mic + music + game':'Mic + game · no music';
  document.getElementById('youtube-music').classList.toggle('excluded',!state.includeMusic);
  document.getElementById('music-point').classList.toggle('off',!state.includeMusic);
  setPlayback(false);state.elapsed=duration;traceRestore=previousElapsed;currentChapter=-1;
  traceElapsed=motion.matches?-1:0;state.traceProgress=motion.matches?-1:0;
  drawFrame();
  showNarration(state.includeMusic?'Music → YouTube.':'Music excluded from YouTube.',state.includeMusic?'Follow the amber signal. The other outputs keep their mixes.':'Twitch keeps its music. The archive keeps the original tracks.');
  story.setAttribute('aria-live','polite');
  lastTime=0;requestTick();
});

motion.addEventListener('change',()=>{
  state.reduced=motion.matches;
  if(motion.matches){setPlayback(false);state.elapsed=duration;currentChapter=-1;drawFrame();syncTransport()}
});
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

try {
  const {createSignalScene}=await import('./scene.js');
  scene=createSignalScene(document.getElementById('three-layer'));
  demo.classList.add('three-ready');demo.dataset.renderer='threejs';
  document.querySelector('#three-layer canvas').addEventListener('webglcontextlost',event=>{
    event.preventDefault();demo.classList.remove('three-ready');demo.dataset.renderer='svg';scene?.dispose();scene=null;
  });
  refreshLayout();
} catch(error) {
  demo.dataset.renderer='svg';
  document.getElementById('render-note').textContent='Static path available. Explore each mix with the switch.';
  refreshLayout();
}
window.addEventListener('pagehide',()=>{cancelAnimationFrame(frameId);observer.disconnect();resizeObserver.disconnect();scene?.dispose()},{once:true});
syncTransport();drawFrame();
