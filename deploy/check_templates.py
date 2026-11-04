#!/usr/bin/env python3
"""Local-only invariants. No subprocesses, network, service or filesystem writes."""
from pathlib import Path
import configparser

root = Path(__file__).resolve().parent
unit = configparser.ConfigParser(interpolation=None, strict=True)
unit.optionxform = str
unit.read(root / "qi-preview.service")
s = unit["Service"]
assert s["DynamicUser"] == "true"
assert s["User"] not in ("root", "0", "")
assert s["Group"] not in ("root", "0", "")
assert s["StateDirectoryMode"] == "0700" and s["UMask"] == "0077"
assert s["NoNewPrivileges"] == "true" and s["ProtectSystem"] == "strict"
assert s["CapabilityBoundingSet"] == "" and s["AmbientCapabilities"] == ""
assert s["EnvironmentFile"] == "/etc/qi-preview/preview.env"
command = s["ExecStart"].split()
assert "-public-preview" in command
assert command[command.index("-listen") + 1] == "127.0.0.1:8686"
assert command[command.index("-db") + 1] == "/var/lib/qi-preview/data/tasks.db"
for flag in ("-enable-workers", "-mcp-tools", "-work-dir", "-artifact-dir", "-skill-root"):
    assert flag not in command
values = {}
for line in (root / "preview.env.example").read_text().splitlines():
    if line and not line.startswith("#"):
        key, value = line.split("=", 1)
        assert key not in values
        values[key] = value
for key in ("QI_PREVIEW_ORIGIN", "QI_WEB_TOKEN", "OPENAI_MODEL", "OPENAI_API_KEY", "OPENAI_BASE_URL"):
    assert values[key] == "", f"Example must not contain a usable value: {key}"
assert not any(k.startswith(("QI_CODEX", "QI_CLAUDE")) for k in values)
caddy = (root / "Caddyfile.example").read_text()
assert 'respond "Preview not activated" 503' in caddy
assert "# reverse_proxy 127.0.0.1:8686" in caddy
active_caddy = "\n".join(line for line in caddy.splitlines() if not line.lstrip().startswith("#"))
assert "reverse_proxy" not in active_caddy
assert 'respond "Preview not activated" 503' in active_caddy
assert "https://PREVIEW_HOST_INVALID" in caddy
print("PASS: local preview template invariants; no deployment performed")
