export async function createDemoAudio() {
  const rate=16000,duration=16,length=rate*duration;
  const stems=[new Float32Array(length),new Float32Array(length),new Float32Array(length)];
  // Read our local, generated PCM narration without creating an AudioContext.
  const wav=await (await fetch(new URL('./media/voice.wav',import.meta.url))).arrayBuffer();
  const view=new DataView(wav);let offset=12,pcmOffset=0,pcmLength=0;
  while(offset+8<=view.byteLength){
    const tag=String.fromCharCode(...new Uint8Array(wav,offset,4));const size=view.getUint32(offset+4,true);
    if(tag==='data'){pcmOffset=offset+8;pcmLength=size;break}offset+=8+size+(size%2);
  }
  if(!pcmOffset)throw new Error('The local narration is not a PCM WAV file.');
  for(let i=0;i<Math.min(length,pcmLength/2);i++)stems[0][i]=view.getInt16(pcmOffset+i*2,true)/32768*.6;
  const notes=[130.8128,103.8262,155.5635,116.5409];let seed=27183;
  for(let i=0;i<length;i++){
    const t=i/rate,chord=Math.floor(t/4),base=notes[chord];
    const beat=t%.5,step=Math.floor(t/.25)%8,f=base*[1,1.25,1.5,2,1.5,1.25,2,1.5][step];
    const env=(1-Math.exp(-(t%.25)*90))*Math.exp(-(t%.25)*7);
    stems[1][i]=.17*env*(Math.sin(2*Math.PI*f*t)+.24*Math.sin(4*Math.PI*f*t))+.045*Math.sin(2*Math.PI*base*.5*t)*Math.exp(-beat*6);
    const burst=t%2,decay=Math.exp(-burst*22);
    seed=(seed*16807)%2147483647;
    stems[2][i]=.12*decay*((seed/2147483647-.5)*.65+Math.sin(2*Math.PI*(85*t-15*burst*burst)))+.035*Math.exp(-((t+.75)%2)*18)*Math.sin(2*Math.PI*780*t);
  }
  let context=null,sources=[],gains=[],master=null,enabled=false,selected=0,currentState=null;
  const buffers=[];
  function masks(state,index) {
    if(index===0)return [1,1,1];
    if(index===1)return [1,state.musicYT?1:0,1];
    if(state.mode==='record')return [0,1,2].map(i=>state.solo<0||state.solo===i?1:0);
    return [1,0,0];
  }
  function sample(track,time) {return stems[track][((Math.floor(time*rate)%length)+length)%length]||0}
  function mix(index,time,state) {return masks(state,index).reduce((sum,on,i)=>sum+on*sample(i,time),0)}
  function setMix(state,index=selected) {
    currentState=state;selected=index;
    if(context)gains.forEach((gain,i)=>{gain.gain.cancelScheduledValues(context.currentTime);gain.gain.setTargetAtTime(masks(state,index)[i],context.currentTime,.02)});
  }
  function startSources(time) {
    sources.forEach(source=>{try{source.stop()}catch{}source.disconnect()});sources=[];
    stems.forEach((_,i)=>{
      const source=context.createBufferSource();source.buffer=buffers[i];source.loop=true;source.connect(gains[i]);source.start(0,((time%duration)+duration)%duration);sources.push(source);
    });
  }
  async function listen(state,index,time) {
    if(!context){
      context=new AudioContext({sampleRate:rate});master=context.createGain();master.gain.value=.32;master.connect(context.destination);
      stems.forEach(data=>{const buffer=context.createBuffer(1,length,rate);buffer.copyToChannel(data,0);buffers.push(buffer);const gain=context.createGain();gain.connect(master);gains.push(gain)});
    }
    await context.resume();enabled=true;setMix(state,index);startSources(time);master.gain.setTargetAtTime(.32,context.currentTime,.02);
  }
  function mute(){enabled=false;if(context)master.gain.setTargetAtTime(0,context.currentTime,.025)}
  function pause(){if(context)master.gain.setTargetAtTime(0,context.currentTime,.025)}
  function resume(time){if(context&&enabled){startSources(time);master.gain.setTargetAtTime(.32,context.currentTime,.025)}}
  function dispose(){mute();sources.forEach(source=>{try{source.stop()}catch{}});context?.close()}
  return {sample,mix,masks,listen,mute,pause,resume,setMix,dispose,get enabled(){return enabled},get selected(){return selected}};
}
