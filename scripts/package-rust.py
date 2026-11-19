#!/usr/bin/env python3
"""Package already-tested native binaries; never publishes or downloads."""
import argparse, hashlib, pathlib, tarfile, tomllib, zipfile
p=argparse.ArgumentParser()
p.add_argument("--platform",required=True,choices=["windows-x86_64","linux-x86_64","linux-aarch64"])
p.add_argument("--bin-dir",type=pathlib.Path,required=True)
p.add_argument("--output",type=pathlib.Path,required=True)
a=p.parse_args()
suffix=".exe" if a.platform.startswith("windows") else ""
files=[a.bin_dir/f"{name}{suffix}" for name in ("wmux","wmuxd")]
for f in files:
    if not f.is_file():p.error(f"binary missing: {f}")
a.output.mkdir(parents=True,exist_ok=True)
version=tomllib.loads(pathlib.Path("Cargo.toml").read_text(encoding="utf-8"))["workspace"]["package"]["version"]
archive=a.output/f"wmux-rust-{version}-{a.platform}{'.zip' if suffix else '.tar.gz'}"
if suffix:
    with zipfile.ZipFile(archive,"w",zipfile.ZIP_DEFLATED) as z:
        for f in files:z.write(f,f.name)
        z.write("LICENSE","LICENSE")
        z.write("docs/rust/installation.md","INSTALL.md")
        z.write("scripts/install-rust.ps1","install.ps1")
else:
    with tarfile.open(archive,"w:gz") as t:
        for f in files:t.add(f,arcname=f.name)
        t.add("LICENSE",arcname="LICENSE")
        t.add("docs/rust/installation.md",arcname="INSTALL.md")
        t.add("scripts/install-rust.sh",arcname="install.sh")
digest=hashlib.sha256(archive.read_bytes()).hexdigest()
checksum=a.output/f"{archive.name}.sha256"
checksum.write_text(f"{digest}  {archive.name}\n")
print(archive)
print(checksum)
