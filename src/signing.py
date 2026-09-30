"""
Stable code signing for the auto daemon launcher.

macOS TCC (Full Disk Access, Accessibility, ...) keys its grants on a binary's
code-signing designated requirement. Running python straight from launchd tied
those grants to Homebrew's ad-hoc signed python binary, whose identity changes
on every upgrade. This module builds a tiny native launcher (launcher/auto_daemon.c)
and signs it with a persistent self-signed certificate held in a dedicated
keychain, so its designated requirement -
    identifier "com.darrenoakey.auto-daemon" and certificate root = H"<cert>"
- is identical across every rebuild and TCC grants survive upgrades.

The keychain password is the only secret; it lives in daz-secrets under
service "auto", account "signing-keychain-password" and is only ever passed to
/usr/bin/security over stdin (never argv, never the environment).
"""

from __future__ import annotations

import os
import re
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
from pathlib import Path

DAZ_SECRETS_CLI = Path.home() / ".local" / "bin" / "daz-secrets"
SECRET_SERVICE = "auto"
SECRET_ACCOUNT = "signing-keychain-password"
SECRET_NOT_FOUND_EXIT = 3

LAUNCHER_IDENTIFIER = "com.darrenoakey.auto-daemon"
CERTIFICATE_NAME = "auto-daemon code signing"
CERTIFICATE_DAYS = 7300

SECURITY = "/usr/bin/security"
CODESIGN = "/usr/bin/codesign"
OPENSSL = "/usr/bin/openssl"
CLANG = "/usr/bin/clang"

COMMAND_TIMEOUT_SECONDS = 60


# ##################################################################
# project root
# absolute path to the auto project directory
def project_root() -> Path:
    return Path(__file__).parent.parent.resolve()


# ##################################################################
# default keychain path
# dedicated keychain (gitignored local/) that holds the signing identity
def default_keychain_path() -> Path:
    return project_root() / "local" / "signing.keychain-db"


# ##################################################################
# default launcher path
# where the built and signed launcher binary is installed
def default_launcher_path() -> Path:
    return project_root() / "local" / "bin" / "auto-daemon"


# ##################################################################
# launcher source paths
# the C source and embedded Info.plist for the launcher
def launcher_source_paths() -> tuple[Path, Path]:
    launcher_dir = project_root() / "launcher"
    return launcher_dir / "auto_daemon.c", launcher_dir / "Info.plist"


# ##################################################################
# run bounded
# runs a command with a hard deadline, killing its whole process group on timeout
def run_bounded(
    command: list[str],
    stdin_bytes: bytes | None = None,
    env: dict[str, str] | None = None,
    timeout: float = COMMAND_TIMEOUT_SECONDS,
) -> subprocess.CompletedProcess:
    process = subprocess.Popen(
        command,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        env=env,
        start_new_session=True,
    )
    try:
        stdout, stderr = process.communicate(input=stdin_bytes, timeout=timeout)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait()
        raise RuntimeError(f"{command[0]} timed out after {timeout}s")
    return subprocess.CompletedProcess(command, process.returncode, stdout, stderr)


# ##################################################################
# run secrets cli
# executes daz-secrets with an empty environment; output stays in memory
def run_secrets_cli(args: list[str], stdin_bytes: bytes | None = None) -> subprocess.CompletedProcess:
    if not DAZ_SECRETS_CLI.exists():
        raise RuntimeError(f"daz-secrets CLI not found at {DAZ_SECRETS_CLI}")
    return run_bounded([str(DAZ_SECRETS_CLI), *args], stdin_bytes=stdin_bytes, env={}, timeout=20)


# ##################################################################
# read keychain password
# returns the exact password bytes, or None when the secret does not exist
def read_keychain_password() -> bytes | None:
    result = run_secrets_cli(["get", SECRET_SERVICE, SECRET_ACCOUNT])
    if result.returncode == 0:
        return result.stdout
    if result.returncode == SECRET_NOT_FOUND_EXIT:
        return None
    raise RuntimeError(f"daz-secrets get failed (exit {result.returncode})")


# ##################################################################
# create keychain password
# generates a random password, stores it via stdin, and verifies the readback
def create_keychain_password() -> bytes:
    password = secrets.token_hex(32).encode("ascii")
    result = run_secrets_cli(["set", SECRET_SERVICE, SECRET_ACCOUNT], stdin_bytes=password)
    if result.returncode != 0:
        raise RuntimeError(f"daz-secrets set failed (exit {result.returncode})")
    stored = read_keychain_password()
    if stored != password:
        raise RuntimeError("daz-secrets readback of signing keychain password did not match")
    return password


# ##################################################################
# ensure keychain password
# returns the persistent password, creating it only when no keychain exists yet
def ensure_keychain_password(keychain: Path) -> bytes:
    password = read_keychain_password()
    if password is not None:
        return password
    if keychain.exists():
        raise RuntimeError(
            f"{keychain} exists but its password is missing from daz-secrets "
            f"({SECRET_SERVICE}/{SECRET_ACCOUNT}); restore the secret or delete the keychain"
        )
    return create_keychain_password()


# ##################################################################
# security command
# runs one /usr/bin/security command through its interactive mode so that the
# keychain password never appears in argv; raises on the "returned N" error line
def security_command(command_line: bytes) -> str:
    result = run_bounded([SECURITY, "-i"], stdin_bytes=command_line + b"\n")
    output = result.stdout.decode("utf-8", "replace") + result.stderr.decode("utf-8", "replace")
    failure = re.search(r"^(\S+): returned (-?\d+)$", output, re.MULTILINE)
    if failure:
        raise RuntimeError(f"security {failure.group(1)} failed with {failure.group(2)}")
    if result.returncode != 0:
        raise RuntimeError(f"security {command_line.split()[0].decode('ascii')} failed with exit {result.returncode}")
    return output


# ##################################################################
# quote for security
# quotes a path argument for security's interactive command parser
def quote_for_security(value: str) -> bytes:
    if re.search(r"[\s\"']", value):
        raise RuntimeError(f"path contains whitespace or quotes and cannot be passed to security: {value}")
    return value.encode("utf-8")


# ##################################################################
# ensure keychain
# creates the dedicated signing keychain if missing and unlocks it
def ensure_keychain(keychain: Path, password: bytes) -> None:
    keychain.parent.mkdir(parents=True, exist_ok=True)
    quoted = quote_for_security(str(keychain))
    if not keychain.exists():
        security_command(b"create-keychain -p " + password + b" " + quoted)
        # no auto-lock: the daemon install must be able to sign unattended
        security_command(b"set-keychain-settings " + quoted)
    security_command(b"unlock-keychain -p " + password + b" " + quoted)


# ##################################################################
# find identity
# returns the SHA-1 of the signing identity in the keychain, or None
def find_identity(keychain: Path) -> str | None:
    result = run_bounded([SECURITY, "find-identity", "-p", "codesigning", str(keychain)])
    output = result.stdout.decode("utf-8", "replace")
    match = re.search(r"\b([0-9A-F]{40})\b\s+\"" + re.escape(CERTIFICATE_NAME) + "\"", output)
    return match.group(1) if match else None


# ##################################################################
# create identity
# generates a self-signed code-signing certificate and imports it into the keychain
def create_identity(keychain: Path, password: bytes) -> str:
    quoted = quote_for_security(str(keychain))
    with tempfile.TemporaryDirectory(prefix="auto-signing-") as temp_dir:
        work = Path(temp_dir)
        os.chmod(work, 0o700)
        config = work / "cert.cnf"
        key = work / "key.pem"
        cert = work / "cert.pem"
        bundle = work / "identity.p12"
        bundle_password_file = work / "p12.pass"
        config.write_text(
            "[req]\n"
            "distinguished_name=dn\n"
            "x509_extensions=v3\n"
            "prompt=no\n"
            "[dn]\n"
            f"CN={CERTIFICATE_NAME}\n"
            "O=darrenoakey\n"
            "[v3]\n"
            "basicConstraints=critical,CA:false\n"
            "keyUsage=critical,digitalSignature\n"
            "extendedKeyUsage=critical,codeSigning\n"
            "subjectKeyIdentifier=hash\n"
        )
        bundle_password = secrets.token_hex(16)
        bundle_password_file.write_text(bundle_password)
        os.chmod(bundle_password_file, 0o600)

        generate = run_bounded([
            OPENSSL, "req", "-x509", "-newkey", "rsa:2048", "-nodes",
            "-keyout", str(key), "-out", str(cert),
            "-days", str(CERTIFICATE_DAYS), "-config", str(config),
        ])
        if generate.returncode != 0:
            raise RuntimeError("openssl req failed to generate the signing certificate")

        export = run_bounded([
            OPENSSL, "pkcs12", "-export",
            "-inkey", str(key), "-in", str(cert), "-out", str(bundle),
            "-name", CERTIFICATE_NAME, "-passout", f"file:{bundle_password_file}",
        ])
        if export.returncode != 0:
            raise RuntimeError("openssl pkcs12 failed to export the signing identity")

        security_command(
            b"import " + quote_for_security(str(bundle)) + b" -k " + quoted
            + b" -P " + bundle_password.encode("ascii") + b" -T " + CODESIGN.encode("ascii")
        )
        # allow codesign to use the key without a GUI prompt
        security_command(b"set-key-partition-list -S apple-tool:,apple: -s -k " + password + b" " + quoted)

    identity = find_identity(keychain)
    if identity is None:
        raise RuntimeError("signing identity was imported but cannot be found in the keychain")
    return identity


# ##################################################################
# ensure identity
# returns the signing identity hash, creating the certificate on first use
def ensure_identity(keychain: Path, password: bytes) -> str:
    ensure_keychain(keychain, password)
    identity = find_identity(keychain)
    if identity is not None:
        return identity
    return create_identity(keychain, password)


# ##################################################################
# build launcher
# compiles the C launcher with warnings as errors into a temporary file
def build_launcher(output: Path) -> None:
    source, info_plist = launcher_source_paths()
    output.parent.mkdir(parents=True, exist_ok=True)
    result = run_bounded([
        CLANG, "-O2", "-Wall", "-Wextra", "-Werror",
        "-o", str(output), str(source),
        "-sectcreate", "__TEXT", "__info_plist", str(info_plist),
    ], timeout=180)
    if result.returncode != 0:
        raise RuntimeError("clang failed to build the launcher:\n" + result.stderr.decode("utf-8", "replace"))


# ##################################################################
# sign binary
# signs a binary with the persistent identity using the fixed identifier
def sign_binary(path: Path, keychain: Path, identity: str) -> None:
    result = run_bounded([
        CODESIGN, "--force", "--sign", identity, "--keychain", str(keychain),
        "--identifier", LAUNCHER_IDENTIFIER, "--timestamp=none", str(path),
    ])
    if result.returncode != 0:
        raise RuntimeError("codesign failed:\n" + result.stderr.decode("utf-8", "replace"))


# ##################################################################
# designated requirement
# returns the designated requirement string codesign reports for a binary
def designated_requirement(path: Path) -> str:
    result = run_bounded([CODESIGN, "--display", "-r-", str(path)])
    if result.returncode != 0:
        raise RuntimeError(f"{path} is not signed")
    output = result.stdout.decode("utf-8", "replace") + result.stderr.decode("utf-8", "replace")
    match = re.search(r"^designated => (.+)$", output, re.MULTILINE)
    if not match:
        raise RuntimeError(f"codesign reported no designated requirement for {path}")
    return match.group(1).strip()


# ##################################################################
# expected requirement
# the designated requirement every build must produce for the given identity
def expected_requirement(identity: str) -> str:
    return f'identifier "{LAUNCHER_IDENTIFIER}" and certificate root = H"{identity.lower()}"'


# ##################################################################
# verify signature
# checks the binary is validly signed and satisfies the expected requirement
def verify_signature(path: Path, identity: str) -> str:
    result = run_bounded([CODESIGN, "--verify", "--strict", str(path)])
    if result.returncode != 0:
        raise RuntimeError(f"{path} failed signature verification:\n" + result.stderr.decode("utf-8", "replace"))
    requirement = designated_requirement(path)
    expected = expected_requirement(identity)
    if requirement != expected:
        raise RuntimeError(f"{path} designated requirement {requirement!r} != expected {expected!r}")
    return requirement


# ##################################################################
# install signed launcher
# builds, signs, verifies, and atomically installs the launcher; returns its requirement
def install_signed_launcher(launcher: Path, keychain: Path, identity: str) -> str:
    launcher.parent.mkdir(parents=True, exist_ok=True)
    staging = launcher.with_name(launcher.name + ".new")
    build_launcher(staging)
    sign_binary(staging, keychain, identity)
    requirement = verify_signature(staging, identity)
    if launcher.exists():
        existing = designated_requirement(launcher)
        if existing != requirement:
            raise RuntimeError(
                f"refusing to replace {launcher}: existing requirement {existing!r} differs from {requirement!r}; "
                "TCC grants would be lost"
            )
    # rename keeps the inode of any running launcher intact
    os.replace(staging, launcher)
    return requirement


# ##################################################################
# ensure signed launcher
# the single entry point install uses: persistent password, keychain, identity, launcher
def ensure_signed_launcher(
    launcher: Path | None = None,
    keychain: Path | None = None,
) -> tuple[Path, str]:
    launcher = launcher or default_launcher_path()
    keychain = keychain or default_keychain_path()
    password = ensure_keychain_password(keychain)
    identity = ensure_identity(keychain, password)
    requirement = install_signed_launcher(launcher, keychain, identity)
    return launcher, requirement


# ##################################################################
# signing status
# human-readable report of the launcher's signature for `auto signing`
def signing_status(launcher: Path | None = None, keychain: Path | None = None) -> str:
    launcher = launcher or default_launcher_path()
    keychain = keychain or default_keychain_path()
    lines = [f"launcher:  {launcher}", f"keychain:  {keychain}"]
    identity = find_identity(keychain) if keychain.exists() else None
    lines.append(f"identity:  {identity or 'missing'}")
    if not launcher.exists():
        lines.append("signature: launcher not built (run: auto install)")
        return "\n".join(lines)
    try:
        requirement = designated_requirement(launcher)
    except RuntimeError as err:
        lines.append(f"signature: {err}")
        return "\n".join(lines)
    lines.append(f"requires:  {requirement}")
    if identity is None:
        lines.append("signature: cannot verify without the identity")
    elif requirement == expected_requirement(identity):
        lines.append("signature: valid and stable (rebuilds keep TCC grants)")
    else:
        lines.append("signature: MISMATCH with the persistent identity")
    return "\n".join(lines)


# ##################################################################
# stable python path
# picks a python interpreter path that survives Homebrew patch upgrades
def stable_python_path(executable: str | None = None) -> str:
    executable = executable or sys.executable
    real = Path(executable).resolve()
    version = f"python{sys.version_info.major}.{sys.version_info.minor}"
    for candidate in (Path("/opt/homebrew/bin") / version, Path("/usr/local/bin") / version):
        if candidate.exists() and candidate.resolve() == real:
            return str(candidate)
    which = shutil.which(version)
    if which and Path(which).resolve() == real:
        return which
    return str(real)
