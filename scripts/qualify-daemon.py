#!/usr/bin/env python3
"""Bounded Debian-container qualification for the observation daemon.

This CI-only fixture creates a private temporary state directory and a small
input tree. It retains finite evidence beneath the workspace. It neither
installs a service nor scans or cleans user-managed paths.
"""

from __future__ import annotations

import hashlib
import json
import os
import pathlib
import platform
import re
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time
from typing import Any


evidence_report_names = (
    "metadata.json",
    "status.json",
    "capabilities.json",
    "input-hashes.json",
    "service-foundations.json",
)
evidence_log_names = ("daemon-tests.jsonl", "daemon.log")
evidence_emit_limit = 64 << 10


def signal_owned_group(process: subprocess.Popen[str], signum: int) -> bool:
    # The group can outlive its leader; do not skip surviving descendants
    # merely because poll() has reaped the direct child.
    try:
        os.killpg(process.pid, signum)
    except ProcessLookupError:
        return False
    return True


def kill_owned_group(process: subprocess.Popen[str]) -> None:
    signal_owned_group(process, signal.SIGKILL)


def run(
    argv: list[str],
    *,
    timeout: float = 60,
    check: bool = True,
    cwd: pathlib.Path | None = None,
) -> subprocess.CompletedProcess[str]:
    process = subprocess.Popen(
        argv,
        cwd=cwd,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    try:
        try:
            stdout, stderr = process.communicate(timeout=timeout)
        except subprocess.TimeoutExpired as error:
            kill_owned_group(process)
            try:
                process.communicate(timeout=8)
            except subprocess.TimeoutExpired as cleanup_error:
                raise RuntimeError(
                    f"command group did not stop: {argv[0]}"
                ) from cleanup_error
            raise RuntimeError(
                f"command exceeded {timeout} seconds: {argv[0]}"
            ) from error
        if check and process.returncode != 0:
            raise subprocess.CalledProcessError(
                process.returncode, argv, stdout, stderr
            )
        return subprocess.CompletedProcess(argv, process.returncode, stdout, stderr)
    finally:
        kill_owned_group(process)
        if process.poll() is None:
            process.wait(timeout=8)


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def mount_details(path: pathlib.Path) -> dict[str, str]:
    return {
        "path": str(path),
        "statfs_type": run(["stat", "-f", "-c", "%T", str(path)]).stdout.strip(),
        "mount": run(
            ["findmnt", "-T", str(path), "-n", "-o", "SOURCE,FSTYPE,OPTIONS"]
        ).stdout.strip(),
    }


def read_os_release() -> dict[str, str]:
    values: dict[str, str] = {}
    for line in pathlib.Path("/etc/os-release").read_text().splitlines():
        if "=" in line and not line.startswith("#"):
            key, value = line.split("=", 1)
            values[key] = value.strip().strip('"')
    return values


def file_hash(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def git_run(
    workspace: pathlib.Path, args: list[str]
) -> subprocess.CompletedProcess[str]:
    return run(
        ["git", "-C", str(workspace), "-c", f"safe.directory={workspace}", *args]
    )


def run_daemon_tests(evidence: pathlib.Path, workspace: pathlib.Path) -> None:
    result_path = evidence / "daemon-tests.jsonl"
    with result_path.open("w") as output:
        process = subprocess.Popen(
            [
                "go",
                "test",
                "-json",
                "-race",
                "-count=1",
                "./internal/daemon",
                "./internal/serviceexec",
                "./internal/userservice",
            ],
            cwd=workspace,
            stdout=output,
            stderr=subprocess.STDOUT,
            text=True,
            start_new_session=True,
        )
        try:
            process.wait(timeout=240)
        except subprocess.TimeoutExpired as error:
            kill_owned_group(process)
            try:
                process.wait(timeout=8)
            except subprocess.TimeoutExpired as cleanup_error:
                raise RuntimeError(
                    "daemon race test group did not stop"
                ) from cleanup_error
            raise RuntimeError("daemon race test exceeded 240 seconds") from error
        finally:
            kill_owned_group(process)
            if process.poll() is None:
                process.wait(timeout=8)
    require(
        result_path.stat().st_size <= 16 << 20, "daemon race output exceeded 16 MiB"
    )
    output = result_path.read_text()
    events: list[dict[str, Any]] = []
    for line in output.splitlines():
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    require(
        process.returncode == 0, f"daemon race test failed: exit={process.returncode}"
    )
    skipped = [
        event.get("Test", "package")
        for event in events
        if event.get("Action") == "skip"
    ]
    require(
        not skipped, f"daemon qualification does not accept skipped tests: {skipped}"
    )
    for test_name in (
        "TestBuildActivityRealProcessDefersOnlyItsRoot",
        "TestAttributionRestartKeepsHistoryAndResetsBaseline",
        "TestAttributionCancellationKeepsCachedReadsAndBaseline",
        "TestExecRunnerDoesNotStartCanceledContextOrInheritEnvironment",
        "TestExecRunnerDrainsFastExitOutputBeforeClassifyingResult",
        "TestExecRunnerCleansDescendantsAfterLeaderExit",
        "TestExecRunnerBoundsInheritedPipeDescendant",
        "TestExecRunnerNonzeroLeaderWithPipeDescendantIsUncertain",
        "TestCleanupGroupWithAndResultClassification",
        "TestRenderLinuxEscapesUnitArguments",
        "TestPreviewContractRefusesEffectClaims",
    ):
        require(
            any(
                event.get("Action") == "pass" and event.get("Test") == test_name
                for event in events
            ),
            f"required daemon control did not pass: {test_name}",
        )


def smoke(binary: pathlib.Path, evidence: pathlib.Path, fixture: pathlib.Path) -> None:
    root = fixture / "r"
    nested = root / "nested"
    nested.mkdir(mode=0o700)
    input_files = {
        root / "keep.txt": b"daemon qualification input\n",
        nested / "keep.txt": b"nested input\n",
    }
    for path, contents in input_files.items():
        path.write_bytes(contents)
        path.chmod(0o600)
    hashes = {str(path.relative_to(root)): file_hash(path) for path in input_files}
    state = fixture / "s"
    log_path = evidence / "daemon.log"
    with log_path.open("w") as log:
        process = subprocess.Popen(
            [
                str(binary),
                "--state-dir",
                str(state),
                "daemon",
                "run",
                "--path",
                str(root),
                "--root",
                str(root),
            ],
            stdout=log,
            stderr=subprocess.STDOUT,
            text=True,
            start_new_session=True,
        )
        sent_sigterm = False
        exited_before_sigterm = False
        try:
            deadline = time.monotonic() + 30
            status: dict[str, Any] | None = None
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise RuntimeError(
                        f"daemon exited before status: {process.returncode}"
                    )
                probe = run(
                    [str(binary), "--state-dir", str(state), "daemon", "status"],
                    timeout=4,
                    check=False,
                )
                if probe.returncode == 0:
                    candidate = json.loads(probe.stdout)
                    if candidate.get("attribution"):
                        status = candidate
                        break
                time.sleep(0.2)
            require(
                status is not None,
                "daemon did not publish an attribution before deadline",
            )
            capabilities = json.loads(
                run(
                    [str(binary), "--state-dir", str(state), "daemon", "capabilities"],
                    timeout=4,
                ).stdout
            )
            require(
                capabilities.get("mutation") is False
                and capabilities.get("proposal_evaluation") is False,
                f"unexpected daemon authority: {capabilities}",
            )
            row = status["attribution"]["rows"][0]
            observation = row["observation"]
            require(
                observation.get("path") == str(root)
                and observation.get("coverage") == "complete"
                and observation.get("files") == 2
                and observation.get("gaps") == 0
                and row.get("delta_bytes") is None
                and row.get("baseline_record_id") is None,
                f"unexpected initial root result: {row}",
            )
            require(
                status.get("state") == "observing", f"unexpected daemon state: {status}"
            )
            (evidence / "status.json").write_text(
                json.dumps(status, indent=2, sort_keys=True) + "\n"
            )
            (evidence / "capabilities.json").write_text(
                json.dumps(capabilities, indent=2, sort_keys=True) + "\n"
            )
        finally:
            try:
                if process.poll() is not None:
                    exited_before_sigterm = True
                    exit_code = process.returncode
                else:
                    sent_sigterm = signal_owned_group(process, signal.SIGTERM)
                    if not sent_sigterm:
                        exited_before_sigterm = True
                    try:
                        exit_code = process.wait(timeout=8)
                    except subprocess.TimeoutExpired as error:
                        kill_owned_group(process)
                        try:
                            process.wait(timeout=4)
                        except subprocess.TimeoutExpired as cleanup_error:
                            raise RuntimeError(
                                "daemon group did not stop"
                            ) from cleanup_error
                        raise RuntimeError(
                            "daemon did not terminate after SIGTERM"
                        ) from error
            finally:
                kill_owned_group(process)
                if process.poll() is None:
                    process.wait(timeout=4)
    require(
        sent_sigterm and not exited_before_sigterm,
        "daemon exited before intended SIGTERM",
    )
    require(exit_code == 0, f"daemon SIGTERM exit={exit_code}")
    require(not (state / "control.sock").exists(), "daemon left control socket behind")
    after = {str(path.relative_to(root)): file_hash(path) for path in input_files}
    require(after == hashes, "daemon modified smoke input")
    (evidence / "input-hashes.json").write_text(
        json.dumps({"before": hashes, "after": after}, indent=2, sort_keys=True) + "\n"
    )


def qualify_service_foundations(
    binary: pathlib.Path,
    evidence: pathlib.Path,
    fixture: pathlib.Path,
    workspace: pathlib.Path,
) -> None:
    # Exercise the actual systemd parser with literal metacharacters, not only
    # a string comparison against the renderer. This executable is never run.
    preview_binary = fixture / 'program $% "quote"'
    shutil.copy2(binary, preview_binary)
    preview = json.loads(
        run(
            [
                str(binary),
                "daemon",
                "service",
                "preview",
                "--platform",
                "linux",
                "--executable",
                str(preview_binary),
                "--state-dir",
                str(fixture / "s"),
                "--path",
                str(fixture / "r"),
            ],
            timeout=10,
            cwd=workspace,
        ).stdout
    )
    require(
        all(
            preview.get(key) is False
            for key in ("definition_written", "commands_executed")
        ),
        "service preview claimed effects",
    )
    require(preview.get("manager") == "systemd-user", "unexpected service manager")
    # Verify only a private fixture copy, never install into a manager search path.
    unit = fixture / "qualification.service"
    unit.write_text(preview["content"])
    runtime_dir = fixture / "runtime"
    runtime_dir.mkdir(mode=0o700)
    verification = run(
        [
            "/usr/bin/env",
            f"XDG_RUNTIME_DIR={runtime_dir}",
            "/usr/bin/systemd-analyze",
            "--user",
            "--man=no",
            "verify",
            str(unit),
        ],
        timeout=15,
        cwd=workspace,
    )
    install_test = run(
        ["/bin/bash", str(workspace / "scripts/install-binary.test.sh")],
        timeout=30,
        cwd=workspace,
    )
    (evidence / "service-foundations.json").write_text(
        json.dumps(
            {
                "preview": preview,
                "unit_syntax_verified": verification.returncode == 0,
                "installer_fixture_test_passed": install_test.returncode == 0,
                "service_manager_invoked": False,
            },
            indent=2,
            sort_keys=True,
        )
        + "\n"
    )


def emit_evidence() -> int:
    require(os.geteuid() != 0, "evidence emission must run as an ordinary user")
    workspace = pathlib.Path(os.environ["GITHUB_WORKSPACE"]).resolve()
    evidence = pathlib.Path(os.environ["SPANWIT_QUALIFICATION_EVIDENCE"])
    require(evidence.is_absolute(), "evidence path must be absolute")
    require(
        os.path.commonpath((str(workspace), str(evidence))) == str(workspace),
        "evidence escapes workspace",
    )
    directory_fd = os.open(evidence, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        directory_stat = os.fstat(directory_fd)
        require(stat.S_ISDIR(directory_stat.st_mode), "evidence is not a directory")
        require(
            directory_stat.st_uid == os.geteuid(), "evidence is not owned by emitter"
        )
        require(
            directory_stat.st_mode & 0o077 == 0, "evidence directory is not private"
        )
        for name in evidence_report_names + evidence_log_names:
            try:
                file_fd = os.open(
                    name,
                    os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK,
                    dir_fd=directory_fd,
                )
            except FileNotFoundError:
                print(f"EVIDENCE_MISSING={name}")
                continue
            try:
                file_stat = os.fstat(file_fd)
                require(
                    stat.S_ISREG(file_stat.st_mode) and file_stat.st_nlink == 1,
                    f"unsafe evidence file: {name}",
                )
                if name in evidence_report_names:
                    require(
                        file_stat.st_size <= evidence_emit_limit,
                        f"oversized evidence report: {name}",
                    )
                    offset = 0
                else:
                    offset = max(0, file_stat.st_size - evidence_emit_limit)
                os.lseek(file_fd, offset, os.SEEK_SET)
                payload = os.read(file_fd, evidence_emit_limit)
                sys.stdout.buffer.write(
                    f"=== {name} ({file_stat.st_size} bytes) ===\n".encode()
                )
                sys.stdout.buffer.write(payload)
                if not payload.endswith(b"\n"):
                    sys.stdout.buffer.write(b"\n")
            finally:
                os.close(file_fd)
    finally:
        os.close(directory_fd)
    return 0


def main() -> int:
    require(sys.platform.startswith("linux"), "qualification requires Linux")
    require(os.geteuid() != 0, "qualification must run as an ordinary user")
    for command in (
        "go",
        "git",
        "lsof",
        "mawk",
        "awk",
        "stat",
        "findmnt",
        "dpkg-query",
        "python3",
        "systemd-analyze",
    ):
        require(shutil.which(command) is not None, f"missing prerequisite: {command}")
    require(
        os.path.realpath(shutil.which("awk") or "") == "/usr/bin/mawk",
        "awk must resolve to mawk",
    )
    os_release = read_os_release()
    require(
        os_release.get("ID") == "debian",
        f"qualification requires Debian userspace: {os_release}",
    )
    workspace = pathlib.Path(os.environ["GITHUB_WORKSPACE"]).resolve()
    evidence = pathlib.Path(os.environ["SPANWIT_QUALIFICATION_EVIDENCE"]).resolve()
    require(workspace in evidence.parents, "evidence must stay beneath the workspace")
    image = os.environ["SPANWIT_CI_IMAGE"]
    require(
        re.fullmatch(r"[^\s]+@sha256:[a-f0-9]{64}", image) is not None,
        "image identity must include a pinned digest",
    )
    commit = git_run(workspace, ["rev-parse", "HEAD"]).stdout.strip()
    git_status = git_run(
        workspace, ["status", "--porcelain", "--untracked-files=all"]
    ).stdout.splitlines()
    require(not git_status, f"qualification source is not clean: {git_status}")
    require(
        commit == os.environ["GITHUB_SHA"], "checked-out commit differs from event SHA"
    )
    os.umask(0o077)
    evidence.mkdir(mode=0o700, parents=True, exist_ok=False)
    fixture = pathlib.Path(tempfile.mkdtemp(prefix="swq-", dir="/tmp"))
    fixture.chmod(0o700)
    try:
        state = fixture / "s"
        root = fixture / "r"
        state.mkdir(mode=0o700)
        root.mkdir(mode=0o700)
        metadata: dict[str, Any] = {
            "qualification": "debian-userspace-container-on-native-linux-host",
            "commit": commit,
            "tree": git_run(workspace, ["rev-parse", "HEAD^{tree}"]).stdout.strip(),
            "github_sha": os.environ.get("GITHUB_SHA", ""),
            "git_status": git_status,
            "workspace": str(workspace),
            "evidence": str(evidence),
            "image": image,
            "diagnostic_packages": run(
                [
                    "dpkg-query",
                    "-W",
                    "-f=${Package}=${Version}\n",
                    "lsof",
                    "mawk",
                    "util-linux",
                    "systemd",
                ]
            ).stdout.splitlines(),
            "os_release": os_release,
            "uname": platform.uname()._asdict(),
            "uid": os.getuid(),
            "gid": os.getgid(),
            "go": run(["go", "version"]).stdout.strip(),
            "awk": run(["awk", "-W", "version"]).stdout.strip(),
            "state_mount": mount_details(state),
            "root_mount": mount_details(root),
        }
        # Preserve platform/identity evidence even when a later control fails.
        (evidence / "metadata.json").write_text(
            json.dumps(metadata, indent=2, sort_keys=True) + "\n"
        )
        binary = evidence / "spanwit"
        run(
            ["go", "build", "-o", str(binary), "./cmd/spanwit"],
            timeout=300,
            cwd=workspace,
        )
        run_daemon_tests(evidence, workspace)
        qualify_service_foundations(binary, evidence, fixture, workspace)
        smoke(binary, evidence, fixture)
    finally:
        # Only remove the short private fixture this process created under /tmp.
        if fixture.parent == pathlib.Path("/tmp") and fixture.name.startswith("swq-"):
            shutil.rmtree(fixture, ignore_errors=True)
    print(f"QUALIFICATION_EVIDENCE={evidence}")
    print(
        "QUALIFICATION_RESULT=PASS; scope=Debian-userspace container on native Linux host"
    )
    return 0


if __name__ == "__main__":
    try:
        if sys.argv[1:] == ["--emit-evidence"]:
            raise SystemExit(emit_evidence())
        if len(sys.argv) != 1:
            raise RuntimeError("unsupported arguments")
        raise SystemExit(main())
    except Exception as error:
        print(f"QUALIFICATION_RESULT=FAIL: {error}", file=sys.stderr)
        raise
