#!/usr/bin/env python3
"""Run the opt-in cloud Send Mail + local IMAP signup activation E2E.

Secrets are read only from an operator dotenv file and passed to child
processes through their environment.  They are never written to reports,
command arguments, or repository files.
"""

from __future__ import annotations

import argparse
import base64
import os
import pathlib
import re
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import time
from urllib.parse import urlparse


EXPECTED_FROM = "no-reply@realtekconnect.com"
STACK = "video-cloud-staging"


class E2EError(RuntimeError):
    pass


def parse_dotenv(path: pathlib.Path) -> dict[str, str]:
    values: dict[str, str] = {}
    if not path.exists():
        return values
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[7:].strip()
        if "=" not in line:
            continue
        key, value = (part.strip() for part in line.split("=", 1))
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
        if key:
            values[key] = value
    return values


def required(values: dict[str, str], name: str) -> str:
    value = os.environ.get(name, values.get(name, "")).strip()
    if not value:
        raise E2EError(f"missing required test setting: {name}")
    return value


def stack_values(workspace: pathlib.Path) -> dict[str, str]:
    return parse_dotenv(workspace / "cloud_env" / "staging" / "linode" / "env" / "stack.env")


def public_url(domain: str, name: str) -> str:
    value = f"https://{domain.strip()}"
    parsed = urlparse(value)
    if parsed.scheme != "https" or not parsed.hostname or parsed.path not in {"", "/"}:
        raise E2EError(f"invalid {name} staging URL")
    return value


def plus_address(mailbox: str, run_id: str) -> str:
    local, separator, domain = mailbox.strip().partition("@")
    if not separator or not local or not domain or "+" in local:
        raise E2EError("IMAP_EMAIL_ADDR must be a plain mailbox address")
    return f"{local}+{run_id}@{domain}"


def imap_connect_host(settings: dict[str, str], sendmail_base_url: str) -> str:
    host = required(settings, "IMAP_SERVER")
    port = int(required(settings, "IMAP_EMAIL_PORT"))
    try:
        socket.getaddrinfo(host, port)
        return ""
    except socket.gaierror:
        fallback = urlparse(sendmail_base_url).hostname or ""
        if not fallback:
            raise E2EError("IMAP_SERVER DNS failed and no safe connect host exists")
        try:
            socket.getaddrinfo(fallback, port)
        except socket.gaierror as exc:
            raise E2EError(
                "IMAP_SERVER and Send Mail connect-host DNS both failed"
            ) from exc
        return fallback


def canonical_sendmail_env(settings: dict[str, str], admin_url: str) -> dict[str, str]:
    raw_base_url = os.environ.get(
        "SENDMAIL_HTTP_BASE_URL",
        settings.get("SENDMAIL_HTTP_BASE_URL", "https://sm.realtekconnect.com"),
    ).strip()
    parsed = urlparse(raw_base_url)
    if (
        parsed.scheme != "https"
        or parsed.hostname != "sm.realtekconnect.com"
        or parsed.port is not None
        or parsed.username is not None
        or parsed.password is not None
        or parsed.path not in {"", "/"}
        or parsed.query
        or parsed.fragment
    ):
        raise E2EError(
            "SENDMAIL_HTTP_BASE_URL must be https://sm.realtekconnect.com"
        )
    return {
        "AUTH_TOKEN_DELIVERY": "sendmail_http",
        "AUTH_TOKEN_BASE_URL": admin_url,
        "SENDMAIL_HTTP_BASE_URL": raw_base_url.rstrip("/"),
        "SENDMAIL_HTTP_BEARER_TOKEN": required(
            settings, "SENDMAIL_HTTP_BEARER_TOKEN"
        ),
        "SENDMAIL_HTTP_TIMEOUT": "15s",
    }


def run_checked(args: list[str], cwd: pathlib.Path, env: dict[str, str], timeout: int = 600) -> None:
    completed = subprocess.run(
        args, cwd=cwd, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, timeout=timeout, check=False,
    )
    if completed.returncode:
        detail = (completed.stderr or completed.stdout).decode(errors="replace")[-1200:]
        for value in env.values():
            if value and len(value) >= 6:
                detail = detail.replace(value, "<redacted>")
        raise E2EError(f"command failed: {args[0]}: {detail.strip()}")


def deployed_image(workspace: pathlib.Path, env: dict[str, str], namespace: str, deployment: str) -> str:
    kubeconfig = env.get("CLOUD_STAGING_K8S_KUBECONFIG") or env.get("KUBECONFIG") or str(workspace / ".artifacts" / "kube" / f"{STACK}-lke.kubeconfig")
    result = subprocess.run(
        ["kubectl", "--kubeconfig", kubeconfig, "-n", namespace, "get", "deployment", deployment, "-o", "jsonpath={.spec.template.spec.containers[0].image}"],
        stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=False, timeout=30,
    )
    image = result.stdout.decode().strip()
    if result.returncode or not image:
        raise E2EError(f"could not resolve deployed image for {deployment}")
    return image


def use_deployed_images(workspace: pathlib.Path, env: dict[str, str]) -> None:
    targets = {
        "LKE_ACCOUNT_MANAGER_IMAGE": (f"{STACK}-account-manager", "account-manager"),
        "LKE_CLOUD_ADMIN_IMAGE": (f"{STACK}-admin", "cloud-admin"),
        "LKE_FRONTEND_IMAGE": (f"{STACK}-frontend", "frontend"),
        "LKE_CLOUD_LOGGER_IMAGE": (f"{STACK}-logger", "cloud-logger"),
        "LKE_VIDEO_CLOUD_IMAGE": (f"{STACK}-video-cloud", "video-cloud-api"),
    }
    for key, (namespace, deployment) in targets.items():
        env[key] = deployed_image(workspace, env, namespace, deployment)

    # Use the current committed Account Manager source so the scoped rollout is
    # anchored to the exact HTTPS delivery adapter under test.
    account_repo = workspace / "repos" / "rtk_account_manager"
    sha = subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=account_repo,
        stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
        check=False, timeout=10,
    ).stdout.decode().strip()
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise E2EError("could not resolve merged Account Manager main commit")
    env["LKE_ACCOUNT_MANAGER_IMAGE"] = (
        f"ghcr.io/hkt999rtk/rtk_account_manager/account-manager:sha-{sha[:12]}"
    )


def temporary_runtime_root(workspace: pathlib.Path, env: dict[str, str]):
    source = workspace / "cloud_env" / "staging" / "runtime"
    if not (source / "env" / "stack.env").is_file():
        raise E2EError("staging LKE runtime environment is unavailable")
    with tempfile.TemporaryDirectory(prefix="rtk-email-e2e-runtime-") as temp:
        runtime = pathlib.Path(temp) / "runtime"
        shutil.copytree(source, runtime)
        yield runtime


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--workspace", default=".")
    parser.add_argument("--env-file", default=str(pathlib.Path.home() / ".env"))
    parser.add_argument("--confirm", required=True)
    parser.add_argument("--skip-deploy", action="store_true")
    parser.add_argument("--run-id", default="")
    args = parser.parse_args()
    if os.environ.get("RUN_LIVE_EMAIL_E2E") != "1":
        raise E2EError("set RUN_LIVE_EMAIL_E2E=1 to run the cloud email E2E")
    if args.confirm != STACK:
        raise E2EError(f"--confirm must equal {STACK}")

    workspace = pathlib.Path(args.workspace).expanduser().resolve()
    account_repo = workspace / "repos" / "rtk_account_manager"
    admin_web = workspace / "repos" / "rtk_cloud_admin" / "web"
    if not (account_repo / "scripts" / "email_signup_imap.py").is_file() or not (admin_web / "package.json").is_file():
        raise E2EError("workspace must include rtk_account_manager and rtk_cloud_admin submodules")

    settings = parse_dotenv(pathlib.Path(args.env_file).expanduser())
    for name in (
        "IMAP_SERVER", "IMAP_EMAIL_ADDR", "IMAP_EMAIL_PASSWORD", "IMAP_EMAIL_PORT", "IMAP_EMAIL_SECURITY", "IMAP_EMAIL_FOLDER",
    ):
        required(settings, name)
    stack = stack_values(workspace)
    admin_url = public_url(required(stack, "CLOUD_ADMIN_DOMAIN"), "Admin Console")
    account_url = public_url(required(stack, "ACCOUNT_MANAGER_DOMAIN"), "Account Manager")
    run_id = args.run_id or time.strftime("%Y%m%dt%H%M%sz", time.gmtime()) + "-" + secrets.token_hex(4)
    if not re.fullmatch(r"[a-z0-9-]{8,64}", run_id):
        raise E2EError("run ID must use lowercase letters, digits, and hyphens")
    recipient = plus_address(required(settings, "IMAP_EMAIL_ADDR"), run_id)
    child_env = os.environ.copy()
    for name in (
        "IMAP_SERVER", "IMAP_EMAIL_ADDR", "IMAP_EMAIL_PASSWORD",
        "IMAP_EMAIL_PORT", "IMAP_EMAIL_SECURITY", "IMAP_EMAIL_FOLDER",
    ):
        child_env[name] = required(settings, name)
    child_env.update(canonical_sendmail_env(settings, admin_url))
    connect_host = imap_connect_host(
        settings, child_env["SENDMAIL_HTTP_BASE_URL"]
    )
    if connect_host:
        child_env["IMAP_CONNECT_HOST"] = connect_host
    # The checked-in staging file retains legacy provider metadata; this E2E
    # must use the current Kubernetes-only deployment path.
    child_env["CLOUD_PROVIDER"] = "lke"
    child_env["RTK_CLOUD_STAGING_PROVIDER"] = "lke"

    if not args.skip_deploy:
        print(f"Configuring Send Mail delivery and waiting for {STACK} rollout...")
        kubeconfig = pathlib.Path(child_env.get("CLOUD_STAGING_K8S_KUBECONFIG") or child_env.get("KUBECONFIG") or workspace / ".artifacts" / "kube" / f"{STACK}-lke.kubeconfig")
        if not kubeconfig.is_file():
            raise E2EError("KUBECONFIG or CLOUD_STAGING_K8S_KUBECONFIG is required for the LKE Send Mail rollout")
        use_deployed_images(workspace, child_env)
        for runtime_root in temporary_runtime_root(workspace, child_env):
            run_checked(
                [
                    "go", "run", "./scripts/go/rtk-cloud", "--",
                    "account-manager-email-deploy",
                    "--workspace", str(workspace),
                    "--env-root", str(runtime_root),
                    "--kubeconfig", str(kubeconfig),
                    "--confirm", STACK,
                ],
                workspace,
                child_env,
            )

    evidence_dir = workspace / ".artifacts" / "e2e_test" / "email-signup" / run_id
    evidence_dir.mkdir(parents=True, exist_ok=False)
    browser_env = {
        **child_env,
        "EMAIL_E2E_ADMIN_BASE_URL": admin_url,
        "EMAIL_E2E_ACCOUNT_MANAGER_BASE_URL": account_url,
        "EMAIL_E2E_SIGNUP_EMAIL": recipient,
        "EMAIL_E2E_SIGNUP_PASSWORD": base64.urlsafe_b64encode(secrets.token_bytes(18)).decode(),
        "EMAIL_E2E_IMAP_HELPER": str(account_repo / "scripts" / "email_signup_imap.py"),
        "EMAIL_E2E_EXPECTED_FROM": EXPECTED_FROM,
        "EMAIL_E2E_RUN_ID": run_id,
        "EMAIL_E2E_EVIDENCE_PATH": str(evidence_dir / "evidence.json"),
        "AUTH_TOKEN_BASE_URL": admin_url,
    }
    print(f"Running cloud signup activation E2E (run {run_id})...")
    run_checked(["npm", "run", "e2e:email-signup-live"], admin_web, browser_env, timeout=360)
    print(f"Cloud Send Mail + local IMAP signup E2E passed (run {run_id}; evidence redacted).")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (E2EError, subprocess.TimeoutExpired) as exc:
        diagnostic = pathlib.Path(".artifacts/e2e_test/email-signup/last-error.txt")
        diagnostic.parent.mkdir(parents=True, exist_ok=True)
        diagnostic.write_text(f"{exc}\n", encoding="utf-8")
        diagnostic.chmod(0o600)
        print(f"Cloud email E2E failed: {exc}", file=sys.stderr)
        raise SystemExit(1)
