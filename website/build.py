"""Build searchable API docs and reuse the shipped console's welcome scene."""
from pathlib import Path
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
out = ROOT / "build/pages"
subprocess.run([sys.executable, "-m", "mkdocs", "build", "--strict"], cwd=ROOT, check=True)
assets = out / "scene"
assets.mkdir(exist_ok=True)
for name in ("auth-scene.css", "auth-scene.js"):
    shutil.copyfile(ROOT / "internal/console/web" / name, assets / name)
shutil.copyfile(ROOT / "website/home.css", assets / "home.css")
shutil.copyfile(ROOT / "website/home.html", out / "index.html")
(out / ".nojekyll").touch()
