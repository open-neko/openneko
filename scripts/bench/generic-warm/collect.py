import subprocess,time,json,threading,os
from pathlib import Path
root=os.environ.get('OPENNEKO_BENCH_OUTPUT','/private/tmp/openneko-warm-implementation')+'/'
Path(root).mkdir(parents=True,exist_ok=True)
samples=[];done=threading.Event()
def monitor():
 while not done.is_set():
  p=subprocess.run(['docker','stats','--no-stream','--format','{{json .}}'],capture_output=True,text=True)
  stats=[]
  for line in p.stdout.splitlines():
   try:
    row=json.loads(line)
    if row['Name'].startswith(('openneko-branch-demo-demo-','openshell-warm-','openshell-work-')):stats.append({k:row[k] for k in ['Name','MemUsage','CPUPerc','PIDs']})
   except Exception:pass
  samples.append({'wallMs':round(time.time()*1000),'containers':stats})
  open(root+'memory-final.json','w').write(json.dumps(samples,indent=2))
  done.wait(1)
t=threading.Thread(target=monitor);t.start()
try:
 with open(root+'live-final.log','w') as out:
  result=subprocess.run(['docker','exec','-e','OPENNEKO_BROKER_PORT=4317','openneko-branch-demo-demo-worker-1','node','--import','tsx/esm','/app/warm-test/run.mts'],stdout=out,stderr=subprocess.STDOUT)
 print('test_exit='+str(result.returncode),flush=True)
finally:done.set();t.join()
