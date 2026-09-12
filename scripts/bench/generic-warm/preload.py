import os, sys, json, time
start=time.monotonic()
import hermes_bootstrap
hermes_bootstrap.harden_import_path()
from run_agent import AIAgent
import model_tools, toolsets
from tools.registry import registry
with open(os.environ['PRELOAD_REPORT'], 'w') as f:
 json.dump({'preload_ms':(time.monotonic()-start)*1000,'builtin_tool_count':len(registry._tools),'has_terminal':'terminal' in registry._tools,'scoped_tool_names':sorted(name for scope in registry._scoped_tools.values() for name in scope)},f)
sys.argv=['hermes','--yolo','acp']
from hermes_cli.main import main
main()
