import os
import signal
import subprocess
import time
from pathlib import Path

import pytest

import signing


# ##################################################################
# temp keychain fixture
# a real keychain in a temp dir with a throwaway password; never the login keychain
@pytest.fixture
def temp_keychain(tmp_path: Path) -> tuple[Path, bytes]:
    keychain = tmp_path / "test.keychain-db"
    password = b"test-keychain-password"
    yield keychain, password
    if keychain.exists():
        subprocess.run([signing.SECURITY, "delete-keychain", str(keychain)], check=False, capture_output=True)


# ##################################################################
# test security command reports failures
# security -i exits 0 even on failure, so the "returned N" line must be caught
def test_security_command_raises_on_failed_subcommand(tmp_path: Path) -> None:
    missing = tmp_path / "missing.keychain-db"
    with pytest.raises(RuntimeError, match="security unlock-keychain failed"):
        signing.security_command(b"unlock-keychain -p x " + str(missing).encode())


# ##################################################################
# test quote for security rejects whitespace
def test_quote_for_security_rejects_whitespace() -> None:
    with pytest.raises(RuntimeError):
        signing.quote_for_security("/tmp/has space/keychain")
    assert signing.quote_for_security("/tmp/plain") == b"/tmp/plain"


# ##################################################################
# test identity is created once and reused
def test_ensure_identity_is_stable(temp_keychain: tuple[Path, bytes]) -> None:
    keychain, password = temp_keychain
    first = signing.ensure_identity(keychain, password)
    assert len(first) == 40
    assert keychain.exists()
    second = signing.ensure_identity(keychain, password)
    assert second == first


# ##################################################################
# test ensure keychain rejects wrong password
def test_ensure_keychain_rejects_wrong_password(temp_keychain: tuple[Path, bytes]) -> None:
    keychain, password = temp_keychain
    signing.ensure_keychain(keychain, password)
    with pytest.raises(RuntimeError, match="unlock-keychain"):
        signing.ensure_keychain(keychain, b"wrong-password")


# ##################################################################
# test rebuilt launcher keeps the same designated requirement
# this is the whole point: TCC grants key on the requirement, not the binary hash
def test_rebuilt_launcher_keeps_designated_requirement(temp_keychain: tuple[Path, bytes], tmp_path: Path) -> None:
    keychain, password = temp_keychain
    identity = signing.ensure_identity(keychain, password)
    launcher = tmp_path / "bin" / "auto-daemon"

    first = signing.install_signed_launcher(launcher, keychain, identity)
    assert first == signing.expected_requirement(identity)
    assert first == f'identifier "{signing.LAUNCHER_IDENTIFIER}" and certificate root = H"{identity.lower()}"'
    first_inode = launcher.stat().st_ino

    second = signing.install_signed_launcher(launcher, keychain, identity)
    assert second == first
    assert launcher.stat().st_ino != first_inode
    assert not launcher.with_name("auto-daemon.new").exists()

    assert signing.verify_signature(launcher, identity) == first
    info = subprocess.run([signing.CODESIGN, "-dvv", str(launcher)], capture_output=True, text=True, check=True)
    assert f"Identifier={signing.LAUNCHER_IDENTIFIER}" in info.stderr
    assert f"Authority={signing.CERTIFICATE_NAME}" in info.stderr


# ##################################################################
# test install refuses to replace a launcher with a different requirement
def test_install_refuses_requirement_change(temp_keychain: tuple[Path, bytes], tmp_path: Path) -> None:
    keychain, password = temp_keychain
    identity = signing.ensure_identity(keychain, password)
    launcher = tmp_path / "bin" / "auto-daemon"
    signing.install_signed_launcher(launcher, keychain, identity)

    other_keychain = tmp_path / "other.keychain-db"
    try:
        other_identity = signing.ensure_identity(other_keychain, password)
        assert other_identity != identity
        with pytest.raises(RuntimeError, match="TCC grants would be lost"):
            signing.install_signed_launcher(launcher, other_keychain, other_identity)
        assert signing.designated_requirement(launcher) == signing.expected_requirement(identity)
    finally:
        subprocess.run([signing.SECURITY, "delete-keychain", str(other_keychain)], check=False, capture_output=True)


# ##################################################################
# test launcher mirrors child exit status and embeds Info.plist
def test_launcher_mirrors_exit_status(tmp_path: Path) -> None:
    launcher = tmp_path / "auto-daemon"
    signing.build_launcher(launcher)
    assert subprocess.run([str(launcher), "/bin/sh", "-c", "exit 3"], check=False).returncode == 3
    assert subprocess.run([str(launcher)], capture_output=True, check=False).returncode == 64
    assert subprocess.run([str(launcher), "/nonexistent/binary"], capture_output=True, check=False).returncode == 127
    plist = subprocess.run(["/usr/bin/otool", "-P", str(launcher)], capture_output=True, text=True, check=True).stdout
    assert signing.LAUNCHER_IDENTIFIER in plist


# ##################################################################
# test launcher forwards SIGTERM to its child
def test_launcher_forwards_sigterm(tmp_path: Path) -> None:
    launcher = tmp_path / "auto-daemon"
    signing.build_launcher(launcher)
    ready = tmp_path / "ready"
    script = f"trap 'exit 42' TERM; touch {ready}; while :; do sleep 0.1; done"
    process = subprocess.Popen([str(launcher), "/bin/sh", "-c", script])
    deadline = time.time() + 10
    while not ready.exists():
        assert time.time() < deadline, "child never started"
        time.sleep(0.05)
    os.kill(process.pid, signal.SIGTERM)
    assert process.wait(timeout=10) == 42


# ##################################################################
# test secrets cli adapter runs with empty environment
# read-only: info never touches secret bytes
def test_run_secrets_cli_info() -> None:
    result = signing.run_secrets_cli(["info"])
    assert result.returncode == 0
    assert b"provider_id" in result.stdout


# ##################################################################
# test stable python path prefers the versioned homebrew symlink
def test_stable_python_path_resolves_to_same_binary() -> None:
    chosen = signing.stable_python_path()
    assert Path(chosen).resolve() == Path(os.sys.executable).resolve()
    assert "Cellar" not in chosen or not Path("/opt/homebrew/bin").exists()


# ##################################################################
# test signing status reports missing launcher
def test_signing_status_without_launcher(tmp_path: Path) -> None:
    report = signing.signing_status(tmp_path / "bin" / "auto-daemon", tmp_path / "none.keychain-db")
    assert "identity:  missing" in report
    assert "launcher not built" in report
