/** Wire geometry shared by the WebGL layer and the SVG fallback. Pure functions, no DOM. */
export const LANE_GAP=4.5;
export const LANES=['video','mic','music','game'];
const CLUSTER_SHIFT=20;

function bezier(p0,p1,p2,p3,n){
  const out=[];
  for(let i=0;i<=n;i++){
    const t=i/n,u=1-t,a=u*u*u,b=3*u*u*t,c=3*u*t*t,d=t*t*t;
    out.push([a*p0[0]+b*p1[0]+c*p2[0]+d*p3[0],a*p0[1]+b*p1[1]+c*p2[1]+d*p3[1]]);
  }
  return out;
}

function roundedPolyline(raw,radius){
  const pts=raw.filter((p,i)=>i===0||Math.hypot(p[0]-raw[i-1][0],p[1]-raw[i-1][1])>.01);
  const out=[pts[0]];
  for(let i=1;i<pts.length-1;i++){
    const a=pts[i-1],b=pts[i],c=pts[i+1];
    const d1=Math.hypot(b[0]-a[0],b[1]-a[1]),d2=Math.hypot(c[0]-b[0],c[1]-b[1]);
    const r=Math.min(radius,d1/2,d2/2);
    const p=[b[0]+(a[0]-b[0])*r/d1,b[1]+(a[1]-b[1])*r/d1];
    const q=[b[0]+(c[0]-b[0])*r/d2,b[1]+(c[1]-b[1])*r/d2];
    for(let s=0;s<=6;s++){
      const t=s/6,u=1-t;
      out.push([u*u*p[0]+2*u*t*b[0]+t*t*q[0],u*u*p[1]+2*u*t*b[1]+t*t*q[1]]);
    }
  }
  out.push(pts[pts.length-1]);
  return out;
}

function finish(pts){
  const cum=[0];
  for(let i=1;i<pts.length;i++)cum.push(cum[i-1]+Math.hypot(pts[i][0]-pts[i-1][0],pts[i][1]-pts[i-1][1]));
  return {pts,cum,length:cum[cum.length-1]};
}

/** Position (and optionally unit tangent) at distance d along a lane. */
export function pointAtDistance(lane,d,out,tan){
  const {pts,cum}=lane;
  d=Math.max(0,Math.min(lane.length,d));
  let lo=0,hi=cum.length-1;
  while(hi-lo>1){const mid=(lo+hi)>>1;if(cum[mid]<=d)lo=mid;else hi=mid}
  const seg=cum[hi]-cum[lo]||1,t=(d-cum[lo])/seg;
  out[0]=pts[lo][0]+(pts[hi][0]-pts[lo][0])*t;
  out[1]=pts[lo][1]+(pts[hi][1]-pts[lo][1])*t;
  if(tan){tan[0]=(pts[hi][0]-pts[lo][0])/seg;tan[1]=(pts[hi][1]-pts[lo][1])/seg}
  return out;
}

/**
 * Four lanes (video, mic, music, game) per connection.
 * Desktop: horizontal S-curves. Phone/tablet: vertical bus with rounded corners. The Twitch and
 * YouTube routes leave the router left and right of centre and the archive route drops straight
 * down the gutter between those two cards, so no route ever crosses a card.
 */
export function buildRoutes(layout){
  return layout.connections.map((connection,index)=>{
    const {start:a,end:b}=connection;
    const lanes=[];
    for(let k=0;k<4;k++){
      const o=(k-1.5)*LANE_GAP;
      let pts;
      if(!connection.vertical){
        const dx=(b.x-a.x)*.5;
        pts=bezier([a.x,a.y+o],[a.x+dx,a.y+o],[b.x-dx,b.y+o],[b.x,b.y+o],72);
      }else{
        const cluster=index===1?-1:index===2?1:0;
        const sx=a.x+cluster*CLUSTER_SHIFT+o,ex=b.x+o;
        const gap=b.y-a.y;
        const ym=cluster===0?a.y+gap/2:a.y+Math.min(gap*.34,22)+(cluster<0?k:3-k)*Math.min(6,gap*.09);
        pts=roundedPolyline([[sx,a.y],[sx,ym],[ex,ym],[ex,b.y]],6);
      }
      lanes.push(finish(pts));
    }
    return {index,lanes};
  });
}
