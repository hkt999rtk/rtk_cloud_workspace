import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


LOAD_ROOT = Path(__file__).resolve().parents[2]
WORKSPACE_ROOT = Path(__file__).resolve().parents[5]
AGGREGATE_TOOL = LOAD_ROOT / "tools" / "aggregate_video_loadtest_two_host.py"
DEPLOY_SCRIPT = LOAD_ROOT / "scripts" / "deploy_video_loadtest_two_host.sh"
REPO_ROOT = WORKSPACE_ROOT


class VideoLoadTestTwoHostTests(unittest.TestCase):
    def test_aggregate_report_combines_two_result_json_files(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            device = root / "device-results.json"
            app_viewer = root / "app-viewer-results.json"
            device_meta = root / "device-metadata.json"
            app_meta = root / "app-viewer-metadata.json"
            output = root / "two-host-load-report.md"

            device.write_text(
                json.dumps(
                    {
                        "run_id": "run-1",
                        "instance_id": "run-1-device",
                        "profile": "smoke",
                        "config": {"api_url": "https://video-cloud-cd.local:8443", "actors": "device"},
                        "metadata": {"contracts_commit": "contracts-commit", "client_commit": "client-commit"},
                        "summary": {"total_operations": 3, "successes": 3, "failures": 0, "skips": 0, "success_rate": 1.0},
                        "actors": {
                            "device": {"operations": 3, "successes": 3, "failures": 0, "skips": 0},
                            "app": {"operations": 0, "successes": 0, "failures": 0, "skips": 0},
                            "viewer": {"operations": 0, "successes": 0, "failures": 0, "skips": 0},
                        },
                        "operations": [
                            {
                                "actor": "device",
                                "name": "device_websocket_owner",
                                "device_id": "load-device-4",
                                "success": True,
                                "evidence": "connected",
                            },
                            {
                                "actor": "device",
                                "name": "websocket_snapshot_metadata",
                                "device_id": "load-device-4",
                                "success": True,
                                "evidence": "metadata_bytes=128",
                            },
                            {
                                "actor": "device",
                                "name": "recording_command_receive",
                                "device_id": "load-device-4",
                                "success": True,
                                "evidence": "event=start_recording actionid=clip-1",
                            },
                            {
                                "actor": "device",
                                "name": "clip_upload",
                                "device_id": "load-device-4",
                                "success": True,
                                "evidence": "clipid=clip-1 bytes=40",
                            },
                            {
                                "actor": "device",
                                "name": "mqtt_light_device_ready_publish",
                                "device_id": "load-device-2",
                                "success": True,
                                "evidence": "topic=devices/_loadtest/run-1/load-device-2/device/ready",
                            },
                            {
                                "actor": "device",
                                "name": "mqtt_light_command_receive",
                                "device_id": "load-device-2",
                                "success": False,
                                "error_detail": "timeout waiting for MQTT message on devices/load-device-2/down/commands",
                            },
                        ],
                    }
                ),
                encoding="utf-8",
            )
            app_viewer.write_text(
                json.dumps(
                    {
                        "run_id": "run-1",
                        "instance_id": "run-1-app-viewer",
                        "profile": "smoke",
                        "config": {"api_url": "https://video-cloud-cd.local:8443", "actors": "app,viewer"},
                        "metadata": {"contracts_commit": "contracts-commit", "client_commit": "client-commit"},
                        "summary": {"total_operations": 7, "successes": 6, "failures": 1, "skips": 1, "success_rate": 6 / 7},
                        "actors": {
                            "device": {"operations": 0, "successes": 0, "failures": 0, "skips": 0},
                            "app": {"operations": 2, "successes": 2, "failures": 0, "skips": 0},
                            "viewer": {"operations": 5, "successes": 4, "failures": 1, "skips": 1},
                        },
                        "operations": [
                            {
                                "actor": "viewer",
                                "name": "webrtc_media_receive",
                                "device_id": "load-device-0",
                                "viewer_id": "viewer-0",
                                "success": True,
                                "evidence": "packets=8 bytes=40",
                            },
                            {
                                "actor": "viewer",
                                "name": "webrtc_media_answer",
                                "device_id": "load-device-5",
                                "viewer_id": "viewer-5",
                                "success": False,
                                "status_code": 400,
                                "error_class": "http",
                                "error_detail": "http 400: device not online",
                            },
                            {
                                "actor": "app",
                                "name": "recording_request",
                                "device_id": "load-device-4",
                                "success": True,
                            },
                            {
                                "actor": "app",
                                "name": "clip_info",
                                "device_id": "load-device-4",
                                "success": True,
                            },
                            {
                                "actor": "app",
                                "name": "clip_total",
                                "device_id": "load-device-4",
                                "success": True,
                            },
                            {
                                "actor": "app",
                                "name": "clip_enum",
                                "device_id": "load-device-4",
                                "success": True,
                            },
                            {
                                "actor": "app",
                                "name": "clip_download_range",
                                "device_id": "load-device-4",
                                "success": True,
                                "evidence": "bytes=16 status=206",
                            },
                            {
                                "actor": "app",
                                "name": "clip_download_invalid_range",
                                "device_id": "load-device-4",
                                "success": True,
                            },
                            {
                                "actor": "app",
                                "name": "clip_delete",
                                "device_id": "load-device-4",
                                "success": True,
                            },
                            {
                                "actor": "app",
                                "name": "mqtt_light_device_ready_receive",
                                "device_id": "load-device-2",
                                "success": True,
                                "evidence": "topic=devices/_loadtest/run-1/load-device-2/device/ready",
                            },
                            {
                                "actor": "app",
                                "name": "mqtt_light_command_result_receive",
                                "device_id": "load-device-2",
                                "success": False,
                                "error_detail": "timeout waiting for MQTT message on devices/load-device-2/up/messages",
                            },
                        ],
                    }
                ),
                encoding="utf-8",
            )
            device_meta.write_text(json.dumps({"host": "client-a.local", "secret_token": "must-not-leak"}), encoding="utf-8")
            app_meta.write_text(json.dumps({"host": "client-b.local"}), encoding="utf-8")

            subprocess.run(
                [
                    "python3",
                    str(AGGREGATE_TOOL),
                    "--input",
                    f"device={device}",
                    "--input",
                    f"app-viewer={app_viewer}",
                    "--metadata",
                    f"device={device_meta}",
                    "--metadata",
                    f"app-viewer={app_meta}",
                    "--output",
                    str(output),
                    "--server-commit",
                    "server-commit",
                    "--binary-sha256",
                    "binary-checksum",
                ],
                cwd=REPO_ROOT,
                check=True,
            )

            report = output.read_text(encoding="utf-8")
            self.assertIn("Total operations | 10", report)
            self.assertIn("Successes | 9", report)
            self.assertIn("Skips | 1", report)
            self.assertIn("Blocks | 0", report)
            self.assertIn("Profile: `smoke`", report)
            self.assertIn("Client commit: `client-commit`", report)
            self.assertIn("client-a.local", report)
            self.assertIn("client-b.local", report)
            self.assertIn("binary-checksum", report)
            self.assertIn("secret_token`: `<redacted>`", report)
            self.assertIn("## Device Owner Lifecycle", report)
            self.assertIn("load-device-4", report)
            self.assertIn("device_websocket_owner", report)
            self.assertIn("websocket_snapshot_metadata", report)
            self.assertIn("## WebRTC Media Device Coverage", report)
            self.assertIn("load-device-5", report)
            self.assertIn("http 400: device not online", report)
            self.assertIn("## Aggregate Coverage Matrix", report)
            self.assertIn("| camera_recording_clip | PASS |", report)
            self.assertIn("## Camera Recording Clip Coverage", report)
            self.assertIn("clip_upload", report)
            self.assertIn("clip_download_range", report)
            self.assertIn("## MQTT IoT Coverage", report)
            self.assertIn("mqtt_light_command_receive", report)
            self.assertIn("timeout waiting for MQTT message", report)
            self.assertIn("## MQTT Readiness Timeline", report)
            self.assertIn("mqtt_light_device_ready_receive", report)
            self.assertNotIn("must-not-leak", report)

    def test_two_host_deploy_dry_run_redacts_tokens_and_assigns_actor_roles(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            binary = Path(tmp) / "rtk-video-loadtest-linux-amd64"
            binary.write_text("#!/bin/sh\n", encoding="utf-8")
            env = os.environ.copy()
            env.update(
                {
                    "VIDEO_CLOUD_LOAD_API_URL": "https://video-cloud-cd.local:8443",
                    "VIDEO_CLOUD_LOAD_ACCOUNT_TOKEN": "account-secret",
                    "VIDEO_CLOUD_LOAD_ADMIN_TOKEN": "admin-secret",
                    "VIDEO_CLOUD_LOAD_DEVICE_TOKEN": "device-secret",
                    "VIDEO_CLOUD_LOAD_DEVICE_TOKENS": '{"load-device-0":"device-map-secret-0","load-device-1":"device-map-secret-1"}',
                    "VIDEO_CLOUD_LOAD_APP_TOKENS": '{"load-device-0":"app-map-secret-0","load-device-1":"app-map-secret-1"}',
                    "VIDEO_CLOUD_LOAD_REFRESH_TOKEN": "refresh-secret",
                    "VIDEO_CLOUD_LOAD_ALLOW_STRESS": "1",
                    "VIDEO_CLOUD_LOAD_ALLOW_SOAK": "1",
                    "VIDEO_CLOUD_LOAD_APP_ROUTE_SET": "functional",
                    "VIDEO_CLOUD_LOAD_DEVICE_ROUTE_SET": "functional",
                    "VIDEO_CLOUD_LOAD_DEVICE_TRANSPORT_SET": "snapshot",
                    "VIDEO_CLOUD_LOAD_VIEWER_ROUTE_SET": "functional",
                    "VIDEO_CLOUD_LOAD_WEBRTC_MEDIA_SET": "rtp",
                    "VIDEO_CLOUD_LOAD_CLIP_SET": "recording-functional",
                    "VIDEO_CLOUD_LOAD_MQTT_SET": "broker",
                    "VIDEO_CLOUD_LOAD_MQTT_DEVICE_PROFILE": "iot",
                    "VIDEO_CLOUD_LOAD_MQTT_IOT_MIX": "light=4,air_conditioner=3,smart_meter=3",
                    "VIDEO_CLOUD_LOAD_MQTT_REQUIRED": "1",
                    "VIDEO_CLOUD_MQTT_ADDR": "mqtt.example:1883",
                    "VIDEO_CLOUD_MQTT_USERNAME": "mqtt-user",
                    "VIDEO_CLOUD_MQTT_PASSWORD": "mqtt-pass",
                    "VIDEO_CLOUD_LOAD_NEGATIVE_SET": "http",
                    "VIDEO_CLOUD_LOAD_NEGATIVE_MALFORMED_PATH": "/__loadtest/malformed_json",
                    "VIDEO_CLOUD_LOAD_NEGATIVE_TIMEOUT_PATH": "/__loadtest/timeout",
                    "VIDEO_CLOUD_LOAD_DEVICE_IDS": "load-device-4",
                    "VIDEO_CLOUD_LOAD_RUN_ID": "run-1",
                }
            )
            proc = subprocess.run(
                [
                    "bash",
                    str(DEPLOY_SCRIPT),
                    "--dry-run",
                    "--binary",
                    str(binary),
                    "--artifact-dir",
                    str(Path(tmp) / "artifacts"),
                ],
                cwd=REPO_ROOT,
                env=env,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                check=True,
            )
            output = proc.stdout + proc.stderr
            self.assertIn("client-a.local", output)
            self.assertIn("client-b.local", output)
            self.assertIn("BatchMode=yes", output)
            self.assertIn("StrictHostKeyChecking=accept-new", output)
            self.assertIn("VIDEO_CLOUD_LOAD_ACTORS=device", output)
            self.assertIn("VIDEO_CLOUD_LOAD_ACTORS=app\\,viewer", output)
            self.assertIn("VIDEO_CLOUD_LOAD_APP_ROUTE_SET=functional", output)
            self.assertIn("VIDEO_CLOUD_LOAD_DEVICE_ROUTE_SET=functional", output)
            self.assertIn("VIDEO_CLOUD_LOAD_DEVICE_TRANSPORT_SET=snapshot", output)
            self.assertIn("VIDEO_CLOUD_LOAD_VIEWER_ROUTE_SET=functional", output)
            self.assertIn("VIDEO_CLOUD_LOAD_WEBRTC_MEDIA_SET=rtp", output)
            self.assertIn("VIDEO_CLOUD_LOAD_CLIP_SET=recording-functional", output)
            self.assertIn("VIDEO_CLOUD_LOAD_ALLOW_STRESS=1", output)
            self.assertIn("VIDEO_CLOUD_LOAD_ALLOW_SOAK=1", output)
            self.assertIn("VIDEO_CLOUD_LOAD_MQTT_SET=broker", output)
            self.assertIn("VIDEO_CLOUD_LOAD_MQTT_DEVICE_PROFILE=iot", output)
            self.assertIn("VIDEO_CLOUD_LOAD_MQTT_IOT_MIX=light=4\\,air_conditioner=3\\,smart_meter=3", output)
            self.assertIn("VIDEO_CLOUD_LOAD_MQTT_REQUIRED=1", output)
            self.assertIn("VIDEO_CLOUD_MQTT_ADDR=mqtt.example:1883", output)
            self.assertIn("VIDEO_CLOUD_LOAD_NEGATIVE_SET=http", output)
            self.assertIn("VIDEO_CLOUD_LOAD_NEGATIVE_MALFORMED_PATH=/__loadtest/malformed_json", output)
            self.assertIn("VIDEO_CLOUD_LOAD_NEGATIVE_TIMEOUT_PATH=/__loadtest/timeout", output)
            self.assertIn("VIDEO_CLOUD_LOAD_DEVICE_IDS=load-device-4", output)
            self.assertIn("stdout.log", output)
            self.assertIn("stderr.log", output)
            self.assertIn("VIDEO_CLOUD_LOAD_DEVICE_ONLINE_MODE=websocket", output)
            self.assertIn("background device role", output)
            self.assertIn("wait for device role", output)
            self.assertIn("<redacted>", output)
            self.assertNotIn("account-secret", output)
            self.assertNotIn("admin-secret", output)
            self.assertNotIn("device-secret", output)
            self.assertNotIn("device-map-secret-0", output)
            self.assertNotIn("device-map-secret-1", output)
            self.assertNotIn("app-map-secret-0", output)
            self.assertNotIn("app-map-secret-1", output)
            self.assertNotIn("refresh-secret", output)
            self.assertNotIn("mqtt-user", output)
            self.assertNotIn("mqtt-pass", output)

    def test_two_host_deploy_fails_before_ssh_when_credentials_are_missing(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            binary = Path(tmp) / "rtk-video-loadtest-linux-amd64"
            binary.write_text("#!/bin/sh\n", encoding="utf-8")
            artifact_dir = Path(tmp) / "artifacts"
            env = os.environ.copy()
            for key in list(env):
                if key.startswith("VIDEO_CLOUD_LOAD_"):
                    env.pop(key)
            proc = subprocess.run(
                ["bash", str(DEPLOY_SCRIPT), "--binary", str(binary), "--artifact-dir", str(artifact_dir)],
                cwd=REPO_ROOT,
                env=env,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.assertNotEqual(proc.returncode, 0)
            self.assertIn("missing required env", proc.stderr)
            report = (artifact_dir / "preflight" / "failure-report.md").read_text(encoding="utf-8")
            report_json = json.loads((artifact_dir / "preflight" / "failure-report.json").read_text(encoding="utf-8"))
            self.assertIn("missing required env", report)
            self.assertEqual(report_json["phase"], "preflight")
            self.assertEqual(report_json["status"], "FAIL")

    def test_two_host_deploy_collects_remote_artifacts_after_actor_failure(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            binary = root / "rtk-video-loadtest-linux-amd64"
            binary.write_text("#!/bin/sh\n", encoding="utf-8")
            artifact_dir = root / "artifacts"
            fake_bin = root / "bin"
            fake_bin.mkdir()
            log_path = root / "commands.log"

            (fake_bin / "ssh").write_text(
                f"""#!/usr/bin/env bash
printf 'ssh %s\\n' "$*" >> {log_path}
cmd="${{@:$#}}"
case "$cmd" in
  *"rtk-video-loadtest run"*) exit 42 ;;
  *) exit 0 ;;
esac
""",
                encoding="utf-8",
            )
            (fake_bin / "scp").write_text(
                f"""#!/usr/bin/env bash
printf 'scp %s\\n' "$*" >> {log_path}
dest="${{@:$#}}"
case "$dest" in
  */metadata.json) mkdir -p "$(dirname "$dest")"; printf '{{"host":"fake"}}\\n' > "$dest" ;;
  */load-results.json) mkdir -p "$(dirname "$dest")"; printf '{{"summary":{{"total_operations":1,"successes":0,"failures":1,"success_rate":0}}}}\\n' > "$dest" ;;
  */load-report.md) mkdir -p "$(dirname "$dest")"; printf '# fake report\\n' > "$dest" ;;
  */stdout.log) mkdir -p "$(dirname "$dest")"; printf 'stdout account-secret device-secret\\n' > "$dest" ;;
  */stderr.log) mkdir -p "$(dirname "$dest")"; printf 'stderr app-map-secret-0\\n' > "$dest" ;;
esac
exit 0
""",
                encoding="utf-8",
            )
            os.chmod(fake_bin / "ssh", 0o755)
            os.chmod(fake_bin / "scp", 0o755)

            env = os.environ.copy()
            env.update(
                {
                    "PATH": f"{fake_bin}{os.pathsep}{env['PATH']}",
                    "VIDEO_CLOUD_LOAD_API_URL": "http://video-cloud-cd.local:18080",
                    "VIDEO_CLOUD_LOAD_ACCOUNT_TOKEN": "account-secret",
                    "VIDEO_CLOUD_LOAD_ADMIN_TOKEN": "admin-secret",
                    "VIDEO_CLOUD_LOAD_DEVICE_TOKEN": "device-secret",
                    "VIDEO_CLOUD_LOAD_RUN_ID": "run-remote-fail",
                    "VIDEO_CLOUD_LOAD_DEVICE_WARMUP_SECONDS": "0",
                    "VIDEO_CLOUD_LOAD_DEVICE_TAIL_SECONDS": "0",
                }
            )
            proc = subprocess.run(
                ["bash", str(DEPLOY_SCRIPT), "--binary", str(binary), "--artifact-dir", str(artifact_dir)],
                cwd=REPO_ROOT,
                env=env,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.assertNotEqual(proc.returncode, 0)
            self.assertTrue((artifact_dir / "device" / "metadata.json").exists())
            self.assertTrue((artifact_dir / "device" / "load-results.json").exists())
            self.assertTrue((artifact_dir / "device" / "load-report.md").exists())
            self.assertTrue((artifact_dir / "device" / "stdout.log").exists())
            self.assertTrue((artifact_dir / "device" / "stderr.log").exists())
            self.assertTrue((artifact_dir / "app-viewer" / "metadata.json").exists())
            self.assertIn("<redacted>", (artifact_dir / "device" / "stdout.log").read_text(encoding="utf-8"))
            self.assertNotIn("account-secret", (artifact_dir / "device" / "stdout.log").read_text(encoding="utf-8"))
            self.assertNotIn("device-secret", (artifact_dir / "device" / "stdout.log").read_text(encoding="utf-8"))
            self.assertIn("load-results.json", log_path.read_text(encoding="utf-8"))

    def test_two_host_deploy_preflight_ssh_failure_creates_local_report(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            binary = root / "rtk-video-loadtest-linux-amd64"
            binary.write_text("#!/bin/sh\n", encoding="utf-8")
            artifact_dir = root / "artifacts"
            fake_bin = root / "bin"
            fake_bin.mkdir()
            (fake_bin / "ssh").write_text(
                "#!/usr/bin/env bash\n"
                "printf 'Host key verification failed.\\n' >&2\n"
                "exit 255\n",
                encoding="utf-8",
            )
            os.chmod(fake_bin / "ssh", 0o755)
            env = os.environ.copy()
            env.update(
                {
                    "PATH": f"{fake_bin}{os.pathsep}{env['PATH']}",
                    "VIDEO_CLOUD_LOAD_API_URL": "http://video-cloud-cd.local:18080",
                    "VIDEO_CLOUD_LOAD_ACCOUNT_TOKEN": "account-secret",
                    "VIDEO_CLOUD_LOAD_ADMIN_TOKEN": "admin-secret",
                    "VIDEO_CLOUD_LOAD_DEVICE_TOKEN": "device-secret",
                    "VIDEO_CLOUD_LOAD_RUN_ID": "run-ssh-fail",
                }
            )
            proc = subprocess.run(
                ["bash", str(DEPLOY_SCRIPT), "--binary", str(binary), "--artifact-dir", str(artifact_dir)],
                cwd=REPO_ROOT,
                env=env,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.assertNotEqual(proc.returncode, 0)
            report = (artifact_dir / "preflight" / "failure-report.md").read_text(encoding="utf-8")
            self.assertIn("SSH preflight failed", report)
            self.assertIn("BatchMode=yes", report)
            self.assertNotIn("account-secret", report)

    def test_two_host_deploy_accepts_relative_sha256sums_path(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPO_ROOT) as tmp:
            root = Path(tmp)
            binary_dir = root / "dist"
            binary_dir.mkdir()
            binary = binary_dir / "rtk-video-loadtest-linux-amd64"
            binary.write_text("#!/bin/sh\n", encoding="utf-8")
            checksum = subprocess.check_output(["shasum", "-a", "256", str(binary)], text=True).split()[0]
            (binary_dir / "SHA256SUMS").write_text(
                f"{checksum}  rtk-video-loadtest-linux-amd64\n",
                encoding="utf-8",
            )
            env = os.environ.copy()
            env.update(
                {
                    "VIDEO_CLOUD_LOAD_API_URL": "http://video-cloud-cd.local:18080",
                    "VIDEO_CLOUD_LOAD_ACCOUNT_TOKEN": "account-secret",
                    "VIDEO_CLOUD_LOAD_ADMIN_TOKEN": "admin-secret",
                    "VIDEO_CLOUD_LOAD_DEVICE_TOKEN": "device-secret",
                    "VIDEO_CLOUD_LOAD_RUN_ID": "run-relative-sha",
                }
            )
            proc = subprocess.run(
                [
                    "bash",
                    str(DEPLOY_SCRIPT),
                    "--dry-run",
                    "--binary",
                    os.path.relpath(binary, REPO_ROOT),
                    "--artifact-dir",
                    str(root / "artifacts"),
                ],
                cwd=REPO_ROOT,
                env=env,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertIn("two-host load-test artifacts", proc.stdout)


if __name__ == "__main__":
    unittest.main()
