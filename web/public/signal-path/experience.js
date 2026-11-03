import {createDemoAudio} from './demo-audio.js';
import {createDemoVideo} from './demo-video.js';

const panel=document.getElementById('experience');
const stage=document.getElementById('stage');
const reducedMotion=matchMedia('(prefers-reduced-motion: reduce)');
const state={mode:'mix',musicYT:true,solo:-1,failed:false,sound:false,listen:0,playing:!reducedMotion.matches,reduced:reducedMotion.matches,time:0,storyTime:0};
let bank=null,field=null,inView=false,raf=0,previous=0,lastPaint=0,storyPhase=-1,storyAuto=true,disposed=false;
const video=createDemoVideo();
const colors=['#69cd8a','#e3b76f','#72bad3'];
const stemScopes=[...document.querySelectorAll('.stem-scope')].map(canvas=>canvas.getContext('2d'));
const mixScopes=[...document.querySelectorAll('.mix-scope')].map(canvas=>canvas.getContext('2d'));
const archiveScopes=[...document.querySelectorAll('[data-archive]')].map(canvas=>canvas.getContext('2d'));
const story=document.getElementById('story');
const tabs=[...document.querySelectorAll('[data-mode][role=tab]')];
const explanations={
  mix:['Your music choices belong to each destination.','Polyemesis builds a stereo mix from the tracks you select. The video passes through without another encode.'],
  record:['Keep the parts, not just the finished mix.','A local recording can retain the original audio tracks. Solo a stem here to hear the control that gives you in editing.'],
  failover:['A source can drop while destination connections stay up.','With failover configured, the source selector can switch to a standby without restarting every destination.']
};

function writeMessage(element,heading,copy) {
  const strong=document.createElement('strong');strong.textContent=heading;
  element.replaceChildren(strong,document.createTextNode(` ${copy}`));
}
function announce(heading,copy) {
  story.setAttribute('aria-live','polite');writeMessage(story,heading,copy);
}
function syncUi() {
  panel.dataset.mode=state.mode;panel.classList.toggle('primary-lost',state.failed&&state.mode==='failover');
  document.getElementById('youtube-music').checked=state.musicYT;
  document.getElementById('yt-mix-name').textContent=state.musicYT?'Full mix':'Music-free mix';
  document.getElementById('yt-mix-tracks').textContent=state.musicYT?'Mic + music + game':'Mic + game';
  document.getElementById('third-title').textContent=state.mode==='record'?'Local recording':'Podcast';
  document.getElementById('third-status').textContent=state.mode==='record'?'Recording':'Connected';
  document.querySelector('[data-listen="2"]').setAttribute('aria-label',state.mode==='record'?'Listen to recording':'Listen to podcast');
  for(const mode of ['mix','record','failover'])document.getElementById(`action-${mode}`).hidden=mode!==state.mode;
  tabs.forEach(tab=>{const selected=tab.dataset.mode===state.mode;tab.setAttribute('aria-selected',String(selected));tab.tabIndex=selected?0:-1});
  panel.setAttribute('aria-labelledby',`tab-${state.mode}`);
  document.getElementById('source-status').textContent=state.failed?'Primary disconnected':'OBS / primary source';
  document.getElementById('connection').textContent=state.failed?'Standby · active':'SRT · connected';
  document.getElementById('standby-state').textContent=state.failed?'active':'ready';
  document.getElementById('drop-source').textContent=state.failed?'Switch back to primary':'Disconnect primary';
  document.getElementById('hub').lastElementChild.textContent=state.failed?'Standby selected. Destinations stay up.':'Same video. Independent audio.';
  document.querySelectorAll('[data-solo]').forEach(button=>button.setAttribute('aria-pressed',String(Number(button.dataset.solo)===state.solo)));
  document.querySelectorAll('[data-listen]').forEach(button=>button.setAttribute('aria-pressed',String(state.sound&&Number(button.dataset.listen)===state.listen)));
  document.querySelectorAll('[data-output]').forEach(output=>output.style.borderBottomColor=state.sound&&Number(output.dataset.output)===state.listen?'#69cd8a':'');
  const sound=document.getElementById('sound');sound.setAttribute('aria-pressed',String(state.sound));
  sound.querySelector('span').textContent=state.sound?`Sound on · ${['Twitch','YouTube',state.mode==='record'?'recording':'podcast'][state.listen]}`:'Hear the difference';
  const pause=document.getElementById('pause');pause.textContent=state.playing?'Ⅱ':'▷';pause.setAttribute('aria-label',state.playing?'Pause motion':'Play motion');pause.disabled=state.reduced;
  panel.dataset.playback=state.playing?'playing':'paused';
  bank?.setMix(state,state.listen);
}

function measureField() {
  const box=stage.getBoundingClientRect();
  const point=id=>{const r=document.getElementById(id).getBoundingClientRect();return {x:r.left-box.left+r.width/2,y:r.top-box.top+r.height/2}};
  const mobile=matchMedia('(max-width:900px)').matches;
  const hub=point('hub');hub.y+=mobile?60:76;
  const archive=state.mode==='record'?[...document.querySelectorAll('[data-archive]')].map(canvas=>{const r=canvas.getBoundingClientRect();return {x:r.left-box.left,y:r.top-box.top+r.height/2}}):null;
  return {width:box.width,height:box.height,mobile,inputs:[0,1,2].map(i=>point(`stem-${i}`)),outputs:[0,1,2].map(i=>point(`out-${i}`)),archive,hub};
}
function refreshField() {field?.resize(measureField());render()}

function drawScope(context,indices,widthScale=1) {
  if(!bank)return;
  const {width,height}=context.canvas;context.clearRect(0,0,width,height);
  context.strokeStyle='#253348';context.lineWidth=1;context.beginPath();context.moveTo(0,height/2);context.lineTo(width,height/2);context.stroke();
  const span=.12;
  const plot=(mask,color,opacity,weight)=>{
    context.globalAlpha=opacity;context.strokeStyle=color;context.lineWidth=weight;context.beginPath();
    for(let x=0;x<width;x+=2){const t=state.time-(1-x/width)*span;let value=0;for(const i of mask)value+=bank.sample(i,t);const y=height/2-value*height*.7*widthScale;if(x===0)context.moveTo(x,y);else context.lineTo(x,y)}context.stroke();
  };
  if(indices.length>1){indices.forEach(i=>plot([i],colors[i],.28,1.25));plot(indices,'#d7e4f5',.82,1.3)}
  else if(indices.length===1)plot(indices,colors[indices[0]],.95,1.5);
  context.globalAlpha=1;
}
function render() {
  if(disposed)return;
  video.paint(state.reduced?3:state.time,state.failed&&state.mode==='failover');
  stemScopes.forEach((context,i)=>drawScope(context,[i],1.4));
  mixScopes.forEach((context,i)=>drawScope(context,bank?bank.masks(state,i).flatMap((on,j)=>on?[j]:[]):[]));
  if(state.mode==='record')archiveScopes.forEach((context,i)=>{context.canvas.style.opacity=state.solo<0||state.solo===i?'1':'.2';drawScope(context,[i],1.5)});
  field?.paint({...state,reduced:state.reduced||!state.playing});
  document.getElementById('story-time').textContent=`00:${String(Math.floor(state.time)%60).padStart(2,'0')}`;
}
function narrateStory() {
  if(state.mode!=='mix'||!storyAuto)return;
  const phase=state.storyTime<3?0:state.storyTime<7?1:2;
  if(phase===storyPhase)return;storyPhase=phase;
  document.getElementById('story-index').textContent=`0${phase+1} / 03`;
  if(phase===0)writeMessage(story,'One upload carries everything.','Each audio track remains available to route.');
  if(phase===1){state.musicYT=false;syncUi();writeMessage(story,'YouTube loses the music.','Watch the amber path fade while Twitch keeps the full mix.')}
  if(phase===2)writeMessage(story,'The podcast keeps only your voice.','One ingest produces three independent mixes. Try listening to each.');
}
function queueFrame() {if(!raf&&state.playing&&inView&&!document.hidden&&!disposed)raf=requestAnimationFrame(animate)}
function animate(now) {
  raf=0;
  if(!state.playing||!inView||document.hidden||disposed)return;
  if(now-lastPaint<32){queueFrame();return}
  const dt=previous?Math.min(.1,(now-previous)/1000):0;previous=now;lastPaint=now;
  state.time+=dt;state.storyTime+=dt;
  narrateStory();render();queueFrame();
}
function setMotion(playing) {
  state.playing=playing&&!state.reduced;previous=0;cancelAnimationFrame(raf);raf=0;
  if(state.playing)bank?.resume(state.time);else bank?.pause();
  syncUi();render();queueFrame();
}
function setMode(mode) {
  storyAuto=false;state.mode=mode;state.failed=false;state.solo=-1;video.invalidate();
  syncUi();writeMessage(document.getElementById('explanation'),...explanations[mode]);
  document.getElementById('story-index').textContent=mode==='record'?'02 / 03':mode==='failover'?'03 / 03':'01 / 03';
  if(mode==='mix')announce('Every platform gets its own mix.','Toggle YouTube’s music. Then compare it with Twitch.');
  if(mode==='record')announce('Three tracks. Still separate.','Audition each original stem without changing either live platform.');
  if(mode==='failover')announce('Ready for a source failure.','Disconnect the primary. The configured standby takes over.');
  refreshField();
}
async function listenTo(index) {
  if(!bank)return;
  try{await bank.listen(state,index,state.time);state.sound=true;state.listen=index;if(!state.playing&&!state.reduced)setMotion(true);syncUi();render()}
  catch{announce('Audio could not start.','You can still explore the visual routing and source switch.');state.sound=false;syncUi()}
}

tabs.forEach((tab,index)=>{
  tab.addEventListener('click',()=>setMode(tab.dataset.mode));
  tab.addEventListener('keydown',event=>{
    let next=index;if(event.key==='ArrowRight')next=(index+1)%tabs.length;else if(event.key==='ArrowLeft')next=(index-1+tabs.length)%tabs.length;else if(event.key==='Home')next=0;else if(event.key==='End')next=tabs.length-1;else return;
    event.preventDefault();tabs[next].focus();setMode(tabs[next].dataset.mode);
  });
});
document.getElementById('youtube-music').addEventListener('change',event=>{
  storyAuto=false;state.musicYT=event.target.checked;syncUi();render();
  announce(state.musicYT?'Music is now in YouTube’s mix.':'YouTube’s mix has no music.','Twitch and the podcast keep their own selections.');
});
document.querySelectorAll('[data-listen]').forEach(button=>button.addEventListener('click',()=>{storyAuto=false;listenTo(Number(button.dataset.listen))}));
document.getElementById('sound').addEventListener('click',()=>{storyAuto=false;if(state.sound){state.sound=false;bank?.mute();syncUi()}else listenTo(state.listen)});
document.getElementById('pause').addEventListener('click',()=>setMotion(!state.playing));
document.getElementById('replay').addEventListener('click',()=>{
  state.mode='mix';state.failed=false;state.musicYT=true;state.storyTime=0;storyPhase=-1;storyAuto=true;state.solo=-1;
  video.invalidate();syncUi();writeMessage(document.getElementById('explanation'),...explanations.mix);narrateStory();refreshField();if(!state.reduced)setMotion(true);
});
document.querySelectorAll('[data-solo]').forEach(button=>button.addEventListener('click',()=>{
  state.solo=Number(button.dataset.solo);state.listen=2;syncUi();render();
  if(state.sound)listenTo(2);else announce(state.solo<0?'All original tracks are available.':`${['Microphone','Music','Game'][state.solo]} isolated in the recording.`,'Use Listen to hear this selection. Live platform mixes stay the same.');
}));
document.getElementById('drop-source').addEventListener('click',()=>{
  state.failed=!state.failed;video.invalidate();syncUi();render();
  announce(state.failed?'Standby selected. Destinations stay connected.':'Primary source selected again.',state.failed?'The video changes to the standby camera. The output connections continue.':'Your original source feeds the existing output connections.');
});
reducedMotion.addEventListener('change',()=>{state.reduced=reducedMotion.matches;if(state.reduced){state.musicYT=false;storyAuto=false;setMotion(false)}else{syncUi();render()}});
document.addEventListener('visibilitychange',()=>{previous=0;cancelAnimationFrame(raf);raf=0;if(document.hidden)bank?.pause();else{if(state.playing)bank?.resume(state.time);queueFrame()}});
const observer=new IntersectionObserver(entries=>{
  inView=entries[0].isIntersecting;previous=0;
  if(!inView){cancelAnimationFrame(raf);raf=0;bank?.pause()}
  else{if(state.playing)bank?.resume(state.time);queueFrame()}
},{threshold:.04});observer.observe(panel);
const resizeObserver=new ResizeObserver(refreshField);resizeObserver.observe(stage);
window.addEventListener('pagehide',()=>{disposed=true;cancelAnimationFrame(raf);observer.disconnect();resizeObserver.disconnect();field?.dispose();bank?.dispose()},{once:true});

syncUi();video.paint(0);document.getElementById('sound').disabled=true;
try{bank=await createDemoAudio();document.getElementById('sound').disabled=false;syncUi();render()}
catch{announce('The audio sample could not load.','The video, routing and failover demonstrations are still available.')}
if(bank){try{const {createSignalField}=await import('./field.js');field=createSignalField(document.getElementById('signal-field'),bank);panel.classList.add('webgl-ready');panel.dataset.renderer='threejs';refreshField()}catch{panel.dataset.renderer='svg'}}
if(state.reduced){state.musicYT=false;storyAuto=false;syncUi();writeMessage(story,'One ingest. Independent mixes.','Explore the controls at your own pace.');render()}
document.fonts.ready.then(refreshField);queueFrame();
