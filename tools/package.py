"""Build and package only the public client and its blank configuration template."""
import datetime
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tarfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
VERSION = "v0.1.0"
TARGETS = [("windows", "amd64"), ("linux", "amd64"), ("linux", "arm64"),
           ("darwin", "amd64"), ("darwin", "arm64")]


def main():
    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    built = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    output = ROOT / "dist" / VERSION
    output.mkdir(parents=True, exist_ok=True)
    archives = []
    for target_os, arch in TARGETS:
        name = f"openopq-client-{VERSION}-{target_os}-{arch}"
        stage = output / name
        stage.mkdir(exist_ok=True)
        executable = "openopq.exe" if target_os == "windows" else "openopq"
        env = dict(os.environ, GOOS=target_os, GOARCH=arch, CGO_ENABLED="0")
        flags = f"-s -w -X openopq/internal/buildinfo.Version={VERSION} -X openopq/internal/buildinfo.Commit={commit} -X openopq/internal/buildinfo.BuiltAt={built}"
        subprocess.run(["go", "build", "-trimpath", "-ldflags", flags, "-o", str(stage / executable), "./cmd/openopq"],
                       cwd=ROOT, env=env, check=True)
        documents = sorted(ROOT.glob("README*.md")) + sorted((ROOT / "docs").glob("guide*.md"))
        documents.append(ROOT / "docs/assets/banner.svg")
        resources = ["Config.example.json", "LICENSE.md", "THIRD-PARTY-NOTICES.txt"] + [p.relative_to(ROOT).as_posix() for p in documents]
        for source in resources:
            destination = stage / source
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_bytes((ROOT / source).read_bytes())
        metadata = {"version": VERSION, "commit": commit, "os": target_os, "arch": arch,
                    "built_at": built, "client_only": True,
                    "binary_sha256": hashlib.sha256((stage / executable).read_bytes()).hexdigest()}
        (stage / "BUILD.json").write_text(json.dumps(metadata, indent=2) + "\n", encoding="utf-8")
        members = [executable, *resources, "BUILD.json"]
        if target_os == "windows":
            archive = output / (name + ".zip")
            with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as target:
                for member in members:
                    target.write(stage / member, name + "/" + member)
        else:
            archive = output / (name + ".tar.gz")
            def normalize(info):
                info.uid = info.gid = 0
                info.uname = info.gname = ""
                info.mode = 0o755 if Path(info.name).name == executable else 0o644
                return info
            with tarfile.open(archive, "w:gz") as target:
                for member in members:
                    target.add(stage / member, arcname=name + "/" + member, filter=normalize)
        archives.append(archive)
        print(f"Packaged {target_os}/{arch}", flush=True)
    (output / "SHA256SUMS.txt").write_text("".join(
        hashlib.sha256(p.read_bytes()).hexdigest() + "  " + p.name + "\n" for p in archives), encoding="ascii")


if __name__ == "__main__":
    main()
