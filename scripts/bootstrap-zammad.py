#!/usr/bin/env python3
"""Initialize the private, pinned Zammad instance for one RTK Cloud environment."""

import argparse
import base64
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request


APP_DEPLOYMENTS = (
    "zammad-railsserver",
    "zammad-scheduler",
    "zammad-websocket",
    "zammad-nginx",
)
PVC_NAMES = (
    "data-zammad-postgres-0",
    "data-zammad-elasticsearch-master-0",
    "data-zammad-redis-0",
)


def kubectl(kubeconfig, namespace, *args, input_text=None):
    command = ["kubectl", "--kubeconfig", str(kubeconfig), "-n", namespace, *args]
    try:
        result = subprocess.run(
            command, input=input_text, text=True, capture_output=True,
            check=True, timeout=180,
        )
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        # kubectl stderr and the Rails input may contain credentials.
        operation = " ".join(args[:3]) if args[0] in ("get", "rollout") else args[0]
        raise RuntimeError(f"kubectl {operation} failed ({type(error).__name__})") from None
    return result.stdout


def cluster_json(kubeconfig, namespace, kind):
    return json.loads(kubectl(kubeconfig, namespace, "get", kind, "-o", "json"))


def ready_inventory(kubeconfig, namespace):
    kubectl(kubeconfig, namespace, "get", "namespace", namespace)
    claims = {item["metadata"]["name"]: item for item in cluster_json(kubeconfig, namespace, "pvc")["items"]}
    missing = [name for name in PVC_NAMES if claims.get(name, {}).get("status", {}).get("phase") != "Bound"]
    if missing:
        raise RuntimeError("Zammad PVCs are not all Bound: " + ", ".join(missing))
    ingress = cluster_json(kubeconfig, namespace, "ingress")["items"]
    if ingress:
        raise RuntimeError("the support namespace has an Ingress; private-only requirement failed")
    deployments = {item["metadata"]["name"]: item for item in cluster_json(kubeconfig, namespace, "deployment")["items"]}
    missing = [name for name in APP_DEPLOYMENTS if deployments.get(name, {}).get("status", {}).get("readyReplicas", 0) < 1]
    if missing:
        raise RuntimeError("Zammad applications are not ready: " + ", ".join(missing))


def rails(kubeconfig, namespace, code):
    return kubectl(
        kubeconfig, namespace, "exec", "-i", "deploy/zammad-railsserver",
        "-c", "zammad-railsserver", "--", "bundle", "exec", "rails", "runner", "-",
        input_text=code,
    )


def ruby(value):
    return json.dumps(value)


def private_file(path):
    if not path.is_file() or path.is_symlink() or path.stat().st_mode & 0o077:
        raise RuntimeError(f"SecretStore file missing or too permissive: {path.name}")
    value = path.read_text().rstrip("\r\n")
    if not value:
        raise RuntimeError(f"SecretStore file is empty: {path.name}")
    return value


def create_private_file(path, value):
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w") as stream:
        stream.write(value + "\n")
        stream.flush()
        os.fsync(stream.fileno())
    os.chmod(path, 0o600)


def local_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


class ZammadAPI:
    def __init__(self, port):
        self.base = f"http://127.0.0.1:{port}"

    def call(self, method, path, *, email, password, payload=None):
        auth = base64.b64encode(f"{email}:{password}".encode()).decode()
        request = urllib.request.Request(
            self.base + path, method=method,
            data=None if payload is None else json.dumps(payload).encode(),
            headers={"Authorization": "Basic " + auth, "Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(request, timeout=60) as response:
                body = response.read()
                return response.status, json.loads(body) if body else None
        except urllib.error.HTTPError as error:
            raise RuntimeError(f"Zammad {method} {path} returned HTTP {error.code}") from None
        except urllib.error.URLError:
            raise RuntimeError(f"Zammad {method} {path} was unavailable") from None


def start_port_forward(kubeconfig, namespace):
    port = local_port()
    process = subprocess.Popen(
        ["kubectl", "--kubeconfig", str(kubeconfig), "-n", namespace,
         "port-forward", "--address", "127.0.0.1", "svc/zammad-nginx", f"{port}:8080"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    for _ in range(60):
        if process.poll() is not None:
            raise RuntimeError("private Zammad port-forward failed")
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=1):
                return process, ZammadAPI(port)
        except OSError:
            time.sleep(1)
    process.terminate()
    raise RuntimeError("private Zammad port-forward timed out")


def prepare_users(kubeconfig, namespace, operator_password, agent_password):
    # Agent password is needed only when issuing a new integration token.
    agent_part = ""
    if agent_password:
        agent_part = f"""
agent = User.lookup(login: 'rtk-zammad-integration')
if agent
  agent.update!(password: {ruby(agent_password)}, active: true, role_ids: [agent_role.id])
else
  User.create!(login: 'rtk-zammad-integration', firstname: 'RTK', lastname: 'Integration', email: 'rtk-zammad-integration@internal.invalid', password: {ruby(agent_password)}, active: true, role_ids: [agent_role.id])
end
"""
    code = f"""
UserInfo.current_user_id = 1
admin_role = Role.lookup(name: 'Admin')
agent_role = Role.lookup(name: 'Agent')
raise 'missing Zammad roles' unless admin_role && agent_role
operator = User.lookup(login: 'rtk-zammad-operator')
if operator
  operator.update!(password: {ruby(operator_password)}, active: true, role_ids: [admin_role.id])
else
  User.create!(login: 'rtk-zammad-operator', firstname: 'RTK', lastname: 'Operator', email: 'rtk-zammad-operator@internal.invalid', password: {ruby(operator_password)}, active: true, role_ids: [admin_role.id])
end
{agent_part}
Setting.set('ticket_last_contact_behaviour', 'based_on_customer_reaction')
puts 'users_ready'
"""
    if "users_ready" not in rails(kubeconfig, namespace, code):
        raise RuntimeError("Zammad operator/Agent setup was not confirmed")


def ensure_group_and_fields(api, operator_password):
    credential = {"email": "rtk-zammad-operator@internal.invalid", "password": operator_password}
    _, groups = api.call("GET", "/api/v1/groups", **credential)
    _, attributes = api.call("GET", "/api/v1/object_manager_attributes", **credential)
    if isinstance(attributes, dict):
        attributes = list(attributes.values())
    known = {item.get("name"): item for item in attributes if isinstance(item, dict)}
    for name in ("rtk_cloud_uuid", "rtk_category"):
        if name in known and (known[name].get("data_type") != "input" or known[name].get("object") != "Ticket"):
            raise RuntimeError(f"existing {name} has incompatible Zammad schema")
    group = next((item for item in groups if item.get("name") == "RTK Support"), None)
    if group is None:
        status, group = api.call("POST", "/api/v1/groups", payload={"name": "RTK Support", "active": True}, **credential)
        if status != 201:
            raise RuntimeError("RTK Support group creation did not return 201")
    created = False
    for name, display, position in (("rtk_cloud_uuid", "RTK Cloud UUID", 1600), ("rtk_category", "RTK Category", 1610)):
        if name in known:
            continue
        status, field = api.call("POST", "/api/v1/object_manager_attributes", payload={
            "name": name, "object": "Ticket", "display": display, "active": True,
            "position": position, "data_type": "input",
            "data_option": {"type": "text", "maxlength": 255},
            "screens": {
                "create_middle": {
                    "ticket.customer": {"shown": True, "required": False, "item_class": "column"},
                    "ticket.agent": {"shown": True, "required": False, "item_class": "column"},
                },
                "edit": {
                    "ticket.customer": {"shown": True, "required": False},
                    "ticket.agent": {"shown": True, "required": False},
                },
            },
        }, **credential)
        if status != 201 or field.get("name") != name:
            raise RuntimeError(f"{name} creation was not confirmed")
        created = True
    if created:
        status, _ = api.call("POST", "/api/v1/object_manager_attributes_execute_migrations", payload={}, **credential)
        if status != 200:
            raise RuntimeError("Zammad object migration was not confirmed")
    return group["id"], created


def restart_applications(kubeconfig, namespace):
    for name in APP_DEPLOYMENTS:
        kubectl(kubeconfig, namespace, "rollout", "restart", f"deployment/{name}")
    for name in APP_DEPLOYMENTS:
        kubectl(kubeconfig, namespace, "rollout", "status", f"deployment/{name}", "--timeout=180s")


def grant_group(kubeconfig, namespace):
    code = """
UserInfo.current_user_id = 1
agent = User.lookup(login: 'rtk-zammad-integration')
group = Group.lookup(name: 'RTK Support')
raise 'missing integration user or group' unless agent && group
grant = UserGroup.find_or_initialize_by(user_id: agent.id, group_id: group.id)
grant.access = 'full'
grant.save!
puts 'group_access_ready'
"""
    if "group_access_ready" not in rails(kubeconfig, namespace, code):
        raise RuntimeError("integration Agent group access was not confirmed")


def verify_unassigned_owner(api, operator_password):
    _, user = api.call(
        "GET", "/api/v1/users/1", email="rtk-zammad-operator@internal.invalid",
        password=operator_password,
    )
    if user.get("id") != 1 or user.get("login") != "-" or user.get("active") is not False:
        raise RuntimeError("Zammad owner ID 1 is not the inactive unassigned user")


def verify_admin_accounts(api, operator_password):
    credential = {"email": "rtk-zammad-operator@internal.invalid", "password": operator_password}
    _, roles = api.call("GET", "/api/v1/roles", **credential)
    admin_id = next((role.get("id") for role in roles if role.get("name") == "Admin"), None)
    if admin_id is None:
        raise RuntimeError("Zammad Admin role is missing")
    _, users = api.call("GET", "/api/v1/users", **credential)
    active_admins = [user.get("login") for user in users if user.get("active") and admin_id in user.get("role_ids", [])]
    if active_admins != ["rtk-zammad-operator"]:
        raise RuntimeError("unexpected active Zammad Admin account; review it before enabling support")


def ensure_token(api, path, agent_password):
    if path.exists():
        token = private_file(path)
    else:
        status, result = api.call(
            "POST", "/api/v1/user_access_token",
            email="rtk-zammad-integration@internal.invalid", password=agent_password,
            payload={"name": "rtk-cloud-admin-bff", "permission": ["ticket.agent"]},
        )
        if status != 200 or not result.get("token"):
            raise RuntimeError("integration Agent token issuance was not confirmed")
        token = result["token"]
        create_private_file(path, token)
    request = urllib.request.Request(
        api.base + "/api/v1/users/me", headers={"Authorization": "Token token=" + token},
    )
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            if response.status != 200:
                raise RuntimeError("integration Agent token validation failed")
    except urllib.error.HTTPError as error:
        raise RuntimeError(f"integration Agent token returned HTTP {error.code}") from None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--environment", required=True, choices=("dev", "staging", "prod"))
    parser.add_argument("--config-root", type=Path, default=Path.home() / ".config/rtk_cloud")
    parser.add_argument("--apply", action="store_true", help="mutate the selected Zammad instance")
    parser.add_argument("--confirm-stack", help="required with --apply: video-cloud-<environment>")
    args = parser.parse_args()
    os.umask(0o077)
    stack = "video-cloud-" + args.environment
    if args.apply and args.confirm_stack != stack:
        parser.error(f"--apply requires --confirm-stack {stack}")
    namespace = stack + "-support"
    kubeconfig = args.config_root / args.environment / "kube/kubeconfig.yaml"
    if not kubeconfig.is_file():
        raise RuntimeError("selected environment kubeconfig is missing")
    ready_inventory(kubeconfig, namespace)
    if not args.apply:
        print(f"PLAN: {stack} private Zammad workloads and three PVCs are ready; no changes made")
        return
    runtime = args.config_root / args.environment / "runtime"
    operator_path = runtime / "zammad-operator-admin-password"
    token_path = runtime / "zammad-integration-token"
    if operator_path.exists():
        operator_password = private_file(operator_path)
    else:
        operator_password = secrets.token_urlsafe(32)
        create_private_file(operator_path, operator_password)
    agent_password = None if token_path.exists() else secrets.token_urlsafe(32)
    prepare_users(kubeconfig, namespace, operator_password, agent_password)
    process, api = start_port_forward(kubeconfig, namespace)
    try:
        verify_admin_accounts(api, operator_password)
        group_id, migrated = ensure_group_and_fields(api, operator_password)
        if migrated:
            process.terminate()
            process.wait(timeout=10)
            restart_applications(kubeconfig, namespace)
            process, api = start_port_forward(kubeconfig, namespace)
        verify_unassigned_owner(api, operator_password)
        grant_group(kubeconfig, namespace)
        ensure_token(api, token_path, agent_password)
        print(f"READY: {stack} RTK Support group_id={group_id} unassigned_owner_id=1")
    finally:
        if process.poll() is None:
            process.terminate()
            process.wait(timeout=10)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError, KeyError, TypeError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        sys.exit(1)
