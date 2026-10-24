package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Kubernetes interpreted the historical Pod command's $$ as a literal $. This
// exact accepted-state repair uses independently checked native process metadata,
// never that malformed marker, PID 1, or the sidecar's renewal signal handler.
func readManagedOpenBaoAuditProcess(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend) (deploymentplan.Object, error) {
	script := `set -eu
version=$(bao version)
case "$version" in 'OpenBao v2.5.'*) ;; *) exit 65 ;; esac
[ "$(id -u)" = 100 ]
[ "$(od -An -tx1 /run/openbao-pki/private/openbao.pid | tr -d ' \n')" = 240a ]
[ "$(stat -c '%u %g %a' /run/openbao-pki/private/openbao.pid)" = '100 1000 600' ]
found=0
for item in /proc/[0-9]*/comm; do
 IFS= read -r comm < "$item" || continue
 [ "$comm" = bao ] || continue
 proc=${item%/comm}
 pid=${proc#/proc/}
 [ "$pid" -gt 1 ]
 [ "$(readlink "$proc/exe")" = /bin/bao ]
 [ "$(tr '\000' '\n' < "$proc/cmdline")" = "$(printf 'bao\nserver\n-config=/openbao/config/file.hcl')" ]
 [ "$(awk '/^Uid:/ {print $2,$3,$4,$5}' "$proc/status")" = '100 100 100 100' ]
 start=$(awk '{print $22}' "$proc/stat")
 exe_sha=$(sha256sum "$proc/exe" | awk '{print $1}')
 case "$pid:$start:$exe_sha" in *[!0-9a-f:]*) exit 66 ;; esac
 found=$((found+1))
 observed_pid=$pid
 observed_start=$start
 observed_sha=$exe_sha
done
[ "$found" -eq 1 ]
printf '{"pid":"%s","start_time":"%s","executable_sha256":"%s","executable":"/bin/bao","uid":"100","pid_marker_sha256":"%s"}\n' "$observed_pid" "$observed_start" "$observed_sha" "$(sha256sum /run/openbao-pki/private/openbao.pid | awk '{print $1}')"
`
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, b, nil, script)
	var proof deploymentplan.Object
	if err != nil || decodeEnvironmentJSON(raw, &proof) != nil || validateManagedOpenBaoAuditProcess(proof) != nil {
		return nil, createProviderRecoveryBlock("original managed OpenBao provider process and historical PID marker cannot be authenticated for audit reload")
	}
	return proof, nil
}

func validateManagedOpenBaoAuditProcess(proof deploymentplan.Object) error {
	bad := func() error {
		return createProviderRecoveryBlock("audit reload requires the exact native provider process, original UID and malformed historical marker")
	}
	if len(proof) != 6 || proof["executable"] != "/bin/bao" || proof["uid"] != "100" || proof["pid_marker_sha256"] != environmentPreflightBytesSHA256([]byte("$\n")) || !environmentPreflightDigest.MatchString(deploymentplan.String(proof["executable_sha256"])) {
		return bad()
	}
	for _, field := range []string{"pid", "start_time"} {
		s := deploymentplan.String(proof[field])
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil || n == 0 || strconv.FormatUint(n, 10) != s || field == "pid" && (n <= 1 || n > 1<<31-1) {
			return bad()
		}
	}
	return nil
}

func reloadManagedTransportOriginalAudit(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime) error {
	r := runtime.ServiceSessionRecovery
	if !managedTransportOpenBaoAuditRepair(r) || r.Candidate == nil {
		return createProviderRecoveryBlock("native audit reload requires the authentic accepted-host configuration repair")
	}
	admitted, _, err := loadAdmittedCreateRecovery(store, runtime.Execution, r.Review)
	if err != nil {
		return err
	}
	root, retained, err := verifyManagedTransportOpenBaoIdentity(ctx, cfg, store, runtime.Execution, runtime.Backend, r, false)
	if err != nil {
		return err
	}
	if deploymentplan.Digest(retained) != admitted.Plan.Materials["accepted-service-core-managed-partial-openbao-retained-prerequisites"] {
		return createProviderRecoveryBlock("native audit reload process or original custody changed since qualification")
	}
	process, err := readManagedOpenBaoAuditProcess(ctx, cfg, runtime.Backend)
	if err != nil {
		return err
	}
	if retained["provider_process_sha256"] != deploymentplan.Digest(process) {
		return createProviderRecoveryBlock("native audit reload provider process proof changed")
	}
	ns := cfg.Values["CLOUD_STACK_NAME"] + "-secrets"
	id := "ConfigMap/" + ns + "/openbao-file"
	var wanted deploymentplan.Resource
	for _, resource := range r.Candidate.Resources {
		if resource.ID == id {
			wanted = resource
		}
	}
	current, err := runtime.Backend.Read(ctx, id)
	if err != nil || current == nil || wanted.UID == "" || deploymentplan.Map(current["metadata"])["uid"] != wanted.UID || !deploymentplan.Matches(wanted.Desired, current) {
		return createProviderRecoveryBlock("native audit reload lacks its exact written original ConfigMap owner")
	}
	expected := deploymentplan.String(deploymentplan.Map(wanted.Desired["data"])["file.hcl"])
	if !strings.HasSuffix(expected, managedTransportOriginalAuditBlock) {
		return createProviderRecoveryBlock("native audit reload lacks its exact original redacted audit policy")
	}
	hclSHA := environmentPreflightBytesSHA256([]byte(expected))
	wait, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	for {
		raw, readErr := runtime.Backend.command(wait, nil, "--namespace", ns, "exec", "openbao-0", "--container", "openbao", "--", "cat", "/openbao/config/file.hcl")
		if readErr == nil && environmentPreflightBytesSHA256(raw) == hclSHA {
			break
		}
		select {
		case <-wait.Done():
			return createProviderRecoveryBlock("native audit reload ConfigMap projection did not reach the exact reviewed HCL bytes")
		case <-time.After(time.Second):
		}
	}
	// Rebind the same Pod/process after the asynchronous projection wait.
	_, again, err := verifyManagedTransportOpenBaoIdentity(ctx, cfg, store, runtime.Execution, runtime.Backend, r, false)
	if err != nil || deploymentplan.Digest(again) != deploymentplan.Digest(retained) {
		return createProviderRecoveryBlock("original OpenBao provider changed before native audit signal")
	}
	intent := deploymentplan.Object{"pod_uid": retained["pod_uid"], "statefulset_uid": retained["statefulset_uid"], "configmap_uid": wanted.UID, "hcl_sha256": hclSHA, "provider_process_sha256": deploymentplan.Digest(process), "signal": "SIGHUP", "audit_path": "/openbao/audit/audit.jsonl", "log_raw": false}
	body, _ := json.Marshal(intent)
	key := "openbao-managed-audit-reload-" + r.Review
	if err := runtime.Execution.BeginEntry(key, "openbao-audit-reload", body, time.Now().UTC()); err != nil {
		return err
	}
	pid, start, exeSHA := deploymentplan.String(process["pid"]), deploymentplan.String(process["start_time"]), deploymentplan.String(process["executable_sha256"])
	script := fmt.Sprintf("set -eu\n[ \"$(readlink /proc/%s/exe)\" = /bin/bao ]\n[ \"$(awk '{print $22}' /proc/%s/stat)\" = %s ]\n[ \"$(awk '/^Uid:/ {print $2,$3,$4,$5}' /proc/%s/status)\" = '100 100 100 100' ]\n[ \"$(tr '\\000' '\\n' < /proc/%s/cmdline)\" = \"$(printf 'bao\\nserver\\n-config=/openbao/config/file.hcl')\" ]\n[ \"$(sha256sum /proc/%s/exe | awk '{print $1}')\" = %s ]\nkill -HUP %s\n", pid, pid, start, pid, pid, pid, exeSHA, pid)
	if _, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, nil, script); err != nil {
		return createProviderRecoveryBlock("native audit reload outcome requires original-Pod observation; signal replay is prohibited")
	}
	for {
		raw, readErr := environmentCreateOpenBaoExec(wait, cfg, runtime.Backend, []byte(root+"\n"), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao audit list -format=json\n")
		var devices deploymentplan.Object
		if readErr == nil && decodeEnvironmentJSON(raw, &devices) == nil {
			device := deploymentplan.Map(devices["deployment-audit/"])
			options := deploymentplan.Map(device["options"])
			if device["type"] == "file" && options["file_path"] == "/openbao/audit/audit.jsonl" && options["log_raw"] == "false" {
				intent["native_audit_sha256"] = environmentPreflightBytesSHA256(raw)
				if err := runtime.Execution.RecordReceipt(key, mustProviderRecoveryJSON(intent)); err != nil {
					return err
				}
				return runtime.Execution.CompleteEntry(key, time.Now().UTC())
			}
		}
		select {
		case <-wait.Done():
			return createProviderRecoveryBlock("native reload did not install the original redacted audit device")
		case <-time.After(time.Second):
		}
	}
}
