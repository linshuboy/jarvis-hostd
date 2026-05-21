from __future__ import annotations

import os
import subprocess
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
INSTALL_SCRIPT = REPO_ROOT / "deploy" / "macos" / "install.sh"


def _write_executable(path: Path, content: str) -> None:
    path.write_text(content, encoding="utf-8")
    path.chmod(0o755)


def test_hostd_macos_install_script_supports_dry_run_and_staging_root(tmp_path: Path) -> None:
    binary_path = tmp_path / "hostd"
    binary_path.write_text("#!/usr/bin/env bash\nexit 0\n", encoding="utf-8")
    binary_path.chmod(0o755)
    stage_root = tmp_path / "stage"
    result = subprocess.run(
        [
            "bash",
            str(INSTALL_SCRIPT),
            "--dry-run",
            "--root",
            str(stage_root),
            "--bin-src",
            str(binary_path),
            "--bin-dst",
            "/Applications/Sunvisai/hostd",
            "--config-dst",
            "/Users/test/Library/Application Support/hostd/config.json",
            "--state-path",
            "/Users/test/Library/Application Support/hostd/state.json",
            "--plist-dst",
            "/Users/test/Library/LaunchAgents/ai.sunvisai.hostd.plist",
        ],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    output = result.stdout
    assert "install -Dm0755" in output
    assert "service install-launchd" in output
    assert "/Applications/Sunvisai/hostd" in output
    assert not stage_root.exists()


def test_hostd_macos_install_script_installs_and_invokes_launchd_cli(tmp_path: Path) -> None:
    binary_path = tmp_path / "hostd"
    binary_path.write_text("binary-v1\n", encoding="utf-8")
    binary_path.chmod(0o755)
    fake_hostd = tmp_path / "fake-hostd"
    command_log = tmp_path / "commands.log"
    _write_executable(
        fake_hostd,
        """#!/usr/bin/env bash
set -euo pipefail
printf '%s\\n' "$0 $*" >> "${HOSTD_FAKE_LOG}"
""",
    )
    stage_root = tmp_path / "stage"
    env = dict(os.environ)
    env["HOSTD_FAKE_LOG"] = str(command_log)
    subprocess.run(
        [
            "bash",
            str(INSTALL_SCRIPT),
            "--root",
            str(stage_root),
            "--bin-src",
            str(binary_path),
            "--bin-dst",
            "/Applications/Sunvisai/hostd",
            "--config-dst",
            "/Users/test/Library/Application Support/hostd/config.json",
            "--state-path",
            "/Users/test/Library/Application Support/hostd/state.json",
            "--plist-dst",
            "/Users/test/Library/LaunchAgents/ai.sunvisai.hostd.plist",
            "--hostd-cmd",
            str(fake_hostd),
        ],
        cwd=REPO_ROOT,
        env=env,
        check=True,
        capture_output=True,
        text=True,
    )
    installed_binary = stage_root / "Applications" / "Sunvisai" / "hostd"
    installed_config = stage_root / "Users" / "test" / "Library" / "Application Support" / "hostd" / "config.json"
    assert installed_binary.exists()
    assert installed_binary.read_text(encoding="utf-8") == "binary-v1\n"
    assert installed_config.exists()
    logged = command_log.read_text(encoding="utf-8")
    assert "service install-launchd" in logged
    assert "--bin /Applications/Sunvisai/hostd" in logged
    assert "--config /Users/test/Library/Application Support/hostd/config.json" in logged
    assert "--state /Users/test/Library/Application Support/hostd/state.json" in logged
    assert "--plist /Users/test/Library/LaunchAgents/ai.sunvisai.hostd.plist" in logged
