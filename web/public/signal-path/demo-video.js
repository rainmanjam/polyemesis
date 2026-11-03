/** One original animated program frame is copied to every video canvas. */
export function createDemoVideo() {
  const buffer=document.createElement('canvas');buffer.width=480;buffer.height=270;
  const c=buffer.getContext('2d',{alpha:false});
  const outputs=[...document.querySelectorAll('canvas.program')].map(canvas=>canvas.getContext('2d',{alpha:false}));
  let previousFrame=-1;
  function paint(time,standby=false) {
    const tick=Math.floor(time*24);
    if(tick===previousFrame)return;previousFrame=tick;
    const t=time;
    c.fillStyle=standby?'#19293c':'#172337';c.fillRect(0,0,480,270);
    // A stylised night drive: the same animated scene and frame number on each output.
    c.fillStyle=standby?'#a7d6d2':'#e4b878';c.beginPath();c.arc(standby?105:365,67,26,0,Math.PI*2);c.fill();
    c.fillStyle='#24354b';c.beginPath();c.moveTo(0,143);for(let x=0;x<=480;x+=30)c.lineTo(x,108+Math.sin(x*.033)*28+Math.cos(x*.061)*13);c.lineTo(480,200);c.lineTo(0,200);c.fill();
    c.fillStyle='#334b60';c.beginPath();c.moveTo(0,170);for(let x=0;x<=480;x+=30)c.lineTo(x,142+Math.sin(x*.025+1)*17);c.lineTo(480,220);c.lineTo(0,220);c.fill();
    c.fillStyle='#18252c';c.fillRect(0,173,480,97);
    const bend=Math.sin(t*.24)*22,center=240+bend;
    c.fillStyle='#111b29';c.beginPath();c.moveTo(center-17,154);c.lineTo(center+17,154);c.lineTo(445,270);c.lineTo(35,270);c.closePath();c.fill();
    c.strokeStyle='#7c9cae';c.lineWidth=1.4;c.beginPath();c.moveTo(center-17,154);c.lineTo(35,270);c.moveTo(center+17,154);c.lineTo(445,270);c.stroke();
    for(let i=0;i<13;i++){
      const p=((i/13+t*.3)%1),q=p*p,y=155+q*116,w=1+q*5,h=2+q*18;
      c.fillStyle='#d8c29a';c.fillRect(center-w/2,y,w,h);
      for(const side of [-1,1]){const x=center+side*(25+q*220);c.fillStyle='#526b70';c.fillRect(x,y-12*q,2+q*3,14*q);c.fillStyle=side<0?'#72bad3':'#e3b76f';c.fillRect(x,y-12*q,3+q*3,3+q*3)}
    }
    // Original streamer's camera inset, deliberately graphic rather than stock footage.
    c.fillStyle='#172336';c.fillRect(12,187,76,66);c.strokeStyle='#8498b7';c.lineWidth=1;c.strokeRect(12.5,187.5,75,65);
    c.fillStyle='#607b95';c.beginPath();c.ellipse(50,220,13,16,0,0,Math.PI*2);c.fill();c.fillStyle='#3e5974';c.beginPath();c.ellipse(50,253,26,21,0,Math.PI,Math.PI*2);c.fill();
    c.strokeStyle='#a6b9cb';c.lineWidth=4;c.beginPath();c.arc(50,217,17,Math.PI,0);c.stroke();c.fillStyle='#b8c7d5';c.fillRect(30,215,5,14);c.fillRect(65,215,5,14);
    c.fillStyle='#0b1421b5';c.fillRect(13,12,138,25);c.fillStyle='#c9d8e9';c.font='10px monospace';c.fillText(standby?'STANDBY CAMERA':'NIGHT DRIVE / LIVE',22,28);
    c.fillStyle='#b9ccdf';c.font='9px monospace';c.fillText(`FRAME ${String(tick).padStart(5,'0')}`,367,254);
    outputs.forEach(context=>context.drawImage(buffer,0,0));
  }
  return {paint,invalidate(){previousFrame=-1}};
}
