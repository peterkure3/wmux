#!/usr/bin/env python3
"""Resource measurements for one real Bash pane; no synthetic agent state."""
import json, os, pathlib, subprocess, time
def wmux(*args):
    return subprocess.check_output(["wmux", *args], text=True).strip()
wmux("new", "resource-measure", "--detach")
state=json.loads(wmux("--json","list"))
workspace=next(w for w in state["workspaces"].values() if w["name"]=="resource-measure")
pane=workspace["focused"]
pid=next(int(p.name) for p in pathlib.Path("/proc").iterdir() if p.name.isdigit() and (p/"comm").exists() and (p/"comm").read_text().strip()=="wmuxd")
clock=os.sysconf("SC_CLK_TCK")
def sample():
    # /proc stat's process name may contain spaces, so split after its final ')'.
    fields=pathlib.Path(f"/proc/{pid}/stat").read_text().rsplit(")",1)[1].split()
    status=dict(line.split(":",1) for line in pathlib.Path(f"/proc/{pid}/status").read_text().splitlines() if ":" in line)
    return time.monotonic(),int(fields[11])+int(fields[12]),int(status["VmRSS"].split()[0])*1024
time.sleep(1)
a=sample();time.sleep(2);b=sample()
idle_cpu=(b[1]-a[1])/clock/(b[0]-a[0])*100
a=sample()
wmux("pane","send","--pane",pane,"-w","resource-measure","--enter","yes WMUX_HEAVY_OUTPUT | head -n 200000")
time.sleep(2);b=sample()
report={"platform":"linux-x86_64","scenario":"one Bash pane, no UI subscriber, 200000 output lines","idle_daemon_cpu_one_core_percent":idle_cpu,"idle_daemon_rss_bytes":a[2],"output_daemon_cpu_one_core_percent":(b[1]-a[1])/clock/(b[0]-a[0])*100,"output_daemon_rss_bytes":b[2],"sample_seconds":b[0]-a[0]}
pathlib.Path("/workspace/build/verification/resources-linux.json").write_text(json.dumps(report,indent=2)+"\n")
wmux("kill-session","resource-measure","--force")
