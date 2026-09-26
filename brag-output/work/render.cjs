const { chromium } = require('/opt/node22/lib/node_modules/playwright');
const { spawn } = require('child_process');
(async()=>{
  const FPS=30, DUR=22.5, NF=Math.round(FPS*DUR);
  const b = await chromium.launch();
  const p = await b.newPage({viewport:{width:1920,height:1080}});
  p.on('pageerror',e=>console.log('ERR',e.message));
  await p.goto('file://'+__dirname+'/index.html'); await p.evaluate(()=>window.ready);
  const ff = spawn('./ffmpeg',['-y','-loglevel','error','-f','image2pipe','-framerate',String(FPS),'-c:v','png','-i','-',
    '-i','music.wav','-c:v','libx264','-preset','slow','-crf','16','-pix_fmt','yuv420p','-profile:v','high','-movflags','+faststart',
    '-c:a','aac','-b:a','192k','-shortest','../brag-raw.mp4'],{stdio:['pipe','inherit','inherit']});
  for(let f=0;f<NF;f++){
    await p.evaluate(t=>window.render(t), f/FPS);
    const buf = await p.screenshot({type:'png'});
    if(!ff.stdin.write(buf)) await new Promise(r=>ff.stdin.once('drain',r));
    if(f%75===0) console.log('frame',f);
  }
  ff.stdin.end(); await new Promise(r=>ff.on('close',r)); await b.close(); console.log('done');
})();
