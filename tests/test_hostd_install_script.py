from __future__ import annotations

import os
import subprocess
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
INSTALL_SCRIPT = REPO_ROOT / "deploy" / "linux" / "install.sh"


def _write_executable(path: Path, content: str) -> None:
    path.write_text(content, encoding="utf-8")
    path.chmod(0o755)


def test_hostd_linux_install_script_supports_dry_run_and_staging_root(tmp_path: Path) -> None:
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
            "/opt/sunvisai/bin/hostd",
            "--config-dst",
            "/opt/sunvisai/etc/config.json",
            "--state-path",
            "/opt/sunvisai/var/state.json",
        ],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    output = result.stdout
    assert "install -Dm0755" in output
    assert "render" in output
    assert "/opt/sunvisai/bin/hostd" in output
    assert "systemctl enable --now hostd.service" in output
    assert not stage_root.exists()


def test_hostd_linux_install_script_installs_into_overridden_paths_without_root(tmp_path: Path) -> None:
    binary_path = tmp_path / "hostd"
    binary_path.write_text("#!/usr/bin/env bash\nexit 0\n", encoding="utf-8")
    binary_path.chmod(0o755)
    fake_bin_dir = tmp_path / "fake-bin"
    fake_bin_dir.mkdir()
    command_log = tmp_path / "commands.log"
    fake_script = """#!/usr/bin/env bash
set -euo pipefail
printf '%s\\n' "$0 $*" >> "${HOSTD_FAKE_LOG}"
"""
    _write_executable(fake_bin_dir / "systemd-sysusers", fake_script)
    _write_executable(fake_bin_dir / "systemd-tmpfiles", fake_script)
    _write_executable(fake_bin_dir / "systemctl", fake_script)
    stage_root = tmp_path / "stage"
    env = dict(os.environ)
    env["PATH"] = f"{fake_bin_dir}:{env.get('PATH', '')}"
    env["HOSTD_FAKE_LOG"] = str(command_log)
    result = subprocess.run(
        [
            "bash",
            str(INSTALL_SCRIPT),
            "--root",
            str(stage_root),
            "--bin-src",
            str(binary_path),
            "--bin-dst",
            "/opt/sunvisai/bin/hostd",
            "--unit-dst",
            "/etc/systemd/system/custom-hostd.service",
            "--sysusers-dst",
            "/usr/lib/sysusers.d/custom-hostd.conf",
            "--tmpfiles-dst",
            "/usr/lib/tmpfiles.d/custom-hostd.conf",
            "--config-dst",
            "/opt/sunvisai/etc/config.json",
            "--state-path",
            "/opt/sunvisai/var/state.json",
        ],
        cwd=REPO_ROOT,
        env=env,
        check=True,
        capture_output=True,
        text=True,
    )
    assert result.stdout == ""
    installed_binary = stage_root / "opt" / "sunvisai" / "bin" / "hostd"
    installed_unit = stage_root / "etc" / "systemd" / "system" / "custom-hostd.service"
    installed_sysusers = stage_root / "usr" / "lib" / "sysusers.d" / "custom-hostd.conf"
    installed_tmpfiles = stage_root / "usr" / "lib" / "tmpfiles.d" / "custom-hostd.conf"
    installed_config = stage_root / "opt" / "sunvisai" / "etc" / "config.json"
    assert installed_binary.exists()
    assert installed_unit.exists()
    assert installed_sysusers.exists()
    assert installed_tmpfiles.exists()
    assert installed_config.exists()
    unit_text = installed_unit.read_text(encoding="utf-8")
    assert "__HOSTD_" not in unit_text
    assert "WorkingDirectory=/opt/sunvisai/var" in unit_text
    assert "ExecStartPre=/opt/sunvisai/bin/hostd config validate --config /opt/sunvisai/etc/config.json --state /opt/sunvisai/var/state.json" in unit_text
    assert "ExecStart=/opt/sunvisai/bin/hostd run --config /opt/sunvisai/etc/config.json --state /opt/sunvisai/var/state.json" in unit_text
    assert "ReadWritePaths=/opt/sunvisai/var" in unit_text
    assert "ReadOnlyPaths=/opt/sunvisai/etc" in unit_text
    tmpfiles_text = installed_tmpfiles.read_text(encoding="utf-8")
    assert "__HOSTD_" not in tmpfiles_text
    assert "d /opt/sunvisai/etc 0755 root root -" in tmpfiles_text
    assert "d /opt/sunvisai/var 0755 hostd hostd -" in tmpfiles_text
    logged = command_log.read_text(encoding="utf-8")
    assert str(stage_root / "usr" / "lib" / "sysusers.d" / "custom-hostd.conf") in logged
    assert f"{fake_bin_dir / 'systemd-tmpfiles'} --create {stage_root / 'usr' / 'lib' / 'tmpfiles.d' / 'custom-hostd.conf'}" in logged
    assert f"{fake_bin_dir / 'systemctl'} daemon-reload" in logged
    assert f"{fake_bin_dir / 'systemctl'} enable --now custom-hostd.service" in logged


def test_hostd_linux_install_script_overwrites_binary_but_preserves_existing_config(tmp_path: Path) -> None:
    old_binary = tmp_path / "old-hostd"
    old_binary.write_text("old-binary\n", encoding="utf-8")
    old_binary.chmod(0o755)
    new_binary = tmp_path / "new-hostd"
    new_binary.write_text("new-binary\n", encoding="utf-8")
    new_binary.chmod(0o755)
    fake_bin_dir = tmp_path / "fake-bin"
    fake_bin_dir.mkdir()
    command_log = tmp_path / "commands.log"
    fake_script = """#!/usr/bin/env bash
set -euo pipefail
printf '%s\\n' "$0 $*" >> "${HOSTD_FAKE_LOG}"
"""
    _write_executable(fake_bin_dir / "systemd-sysusers", fake_script)
    _write_executable(fake_bin_dir / "systemd-tmpfiles", fake_script)
    _write_executable(fake_bin_dir / "systemctl", fake_script)
    stage_root = tmp_path / "stage"
    config_path = stage_root / "opt" / "sunvisai" / "etc" / "config.json"
    installed_binary = stage_root / "opt" / "sunvisai" / "bin" / "hostd"
    installed_binary.parent.mkdir(parents=True, exist_ok=True)
    installed_binary.write_text(old_binary.read_text(encoding="utf-8"), encoding="utf-8")
    installed_binary.chmod(0o755)
    config_path.parent.mkdir(parents=True, exist_ok=True)
    config_path.write_text('{"display_name":"custom-host"}\n', encoding="utf-8")
    env = dict(os.environ)
    env["PATH"] = f"{fake_bin_dir}:{env.get('PATH', '')}"
    env["HOSTD_FAKE_LOG"] = str(command_log)
    subprocess.run(
        [
            "bash",
            str(INSTALL_SCRIPT),
            "--root",
            str(stage_root),
            "--bin-src",
            str(new_binary),
            "--bin-dst",
            "/opt/sunvisai/bin/hostd",
            "--config-dst",
            "/opt/sunvisai/etc/config.json",
            "--state-path",
            "/opt/sunvisai/var/state.json",
        ],
        cwd=REPO_ROOT,
        env=env,
        check=True,
        capture_output=True,
        text=True,
    )
    assert installed_binary.read_text(encoding="utf-8") == "new-binary\n"
    assert config_path.read_text(encoding="utf-8") == '{"display_name":"custom-host"}\n'
