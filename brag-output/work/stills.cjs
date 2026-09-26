const { chromium } = require('/opt/node22/lib/node_modules/playwright');
(async()=>{
  const b = await chromium.launch();
  const p = await b.newPage({viewport:{width:1920,height:1080}});
  p.on('console',m=>console.log('console:',m.text())); p.on('pageerror',e=>console.log('ERR',e.message));
  await p.goto('file://'+__dirname+'/index.html'); await p.evaluate(()=>window.ready);
  const ts = process.argv.slice(2).map(Number);
  for (const t of ts){ await p.evaluate(t=>window.render(t),t); await p.screenshot({path:`still-${t.toFixed(2)}.png`}); }
  await b.close();
})();
