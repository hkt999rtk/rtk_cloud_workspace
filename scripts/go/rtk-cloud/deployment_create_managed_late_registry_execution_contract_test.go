package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Every permitted command has an exact request and a synthetic native reply.
// Unexpected commands, including every mutation, fail at the protocol boundary.
func lateNativeCommandContractBackend(t *testing.T, store secretStore, replies map[string][]byte) (environmentKubernetesBackend, string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nset -eu\ntest \"$1\" = --kubeconfig && test -f \"$2\" || exit 71\ncase \"$3\" in --request-timeout=*) ;; *) exit 72;; esac\nshift 3\nprintf '%s\\n' \"$*\" >> " + shellQuoteArg(calls) + "\n"
	index := 0
	for command, reply := range replies {
		path := filepath.Join(dir, fmt.Sprintf("response-%d.json", index))
		index++
		if err := os.WriteFile(path, reply, 0600); err != nil {
			t.Fatal(err)
		}
		script += "if test \"$*\" = " + shellQuoteArg(command) + "; then cat " + shellQuoteArg(path) + "; exit 0; fi\n"
	}
	script += "exit 73\n"
	bin := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", bin)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-registry-kubeconfig"), RolloutTimeout: time.Second}, calls
}
func lateRegistryContractCommand(stack, query string) string {
	return strings.Join([]string{"--namespace", stack + "-platform", "exec", "postgresql-0", "--container", "postgres", "--", "env", "PGOPTIONS=-c default_transaction_read_only=on", "/bin/sh", "-ec", `PGPASSWORD="$POSTGRES_PASSWORD" psql -U postgres -d video_cloud -v ON_ERROR_STOP=1 -At -c "$1"`, "registry-read", query}, " ")
}

func TestManagedLateNativeRegistryCRLsExecutionContract(t *testing.T) {
	for _, mode := range []string{"valid", "duplicate-row", "unknown-row", "missing-row", "wrong-digest", "wrong-signature", "expired", "malformed", "duplicate-authority", "invalid-authority", "non-repair-paired"} {
		t.Run(mode, func(t *testing.T) {
			store, e, _, _ := freshStageFixture(t)
			cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack}}
			roots := map[string]environmentCreateRegisteredIssuer{}
			intermediates := map[string]environmentCreateRegisteredIssuer{}
			rows := []managedLateCRLRecord{}
			ids := []string{}
			index := 0
			for _, domain := range []string{"service", "device", "mqtt", "openbao_tls", "app"} {
				count := 2
				if domain == "device" {
					count = 1
				}
				for generation := 0; generation < count; generation++ {
					index++
					issuer, key, _, now := rootCRLFixture(t)
					issuer.ID = fmt.Sprintf("%08d-1111-4111-8111-111111111111", index)
					issuer.Domain = domain
					request := createRootCRLRequest{issuer.ID, issuer.RootFingerprint, "2", now.Add(-time.Hour), now.Add(24 * time.Hour), nil}
					if mode == "expired" && index == 9 {
						request.ThisUpdate = now.Add(-48 * time.Hour)
						request.NextUpdate = now.Add(-24 * time.Hour)
					}
					record := signedRootCRLFixture(t, issuer, key, request)
					rows = append(rows, managedLateCRLRecord{record.IssuerID, record.Digest, record.Number, record.PEM, record.ThisUpdate, record.NextUpdate})
					ids = append(ids, "'"+issuer.ID+"'")
					if generation == 0 {
						roots[domain] = issuer
					} else {
						intermediates[domain] = issuer
					}
				}
			}
			slices.Sort(ids)
			mqtt, tls, mi, ti := roots["mqtt"], roots["openbao_tls"], intermediates["mqtt"], intermediates["openbao_tls"]
			recovery := &createServiceSessionRecovery{Ceremony: environmentCreateServiceCeremony{Root: roots["service"], Intermediate: intermediates["service"]}, ManagedTransportRecovery: &createManagedTransportRecovery{Device: roots["device"], AppRoot: roots["app"], AppIntermediate: intermediates["app"]}, MQTTRoot: &mqtt, OpenBaoRoot: &tls, MQTTIntermediate: &mi, OpenBaoIntermediate: &ti}
			switch mode {
			case "duplicate-row":
				rows[8] = rows[0]
			case "unknown-row":
				rows[8].IssuerID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "missing-row":
				rows = rows[:8]
			case "wrong-digest":
				rows[8].Digest = strings.Repeat("f", 64)
			case "wrong-signature":
				rows[8].PEM = rows[0].PEM
			case "duplicate-authority":
				recovery.Ceremony.Intermediate = recovery.Ceremony.Root
			case "invalid-authority":
				recovery.Ceremony.Intermediate.ID = "not-a-native-uuid"
			}
			raw := mustProviderRecoveryJSON(rows)
			if mode == "malformed" {
				raw = []byte(`{"private-fixture":"not-array"}`)
			}
			query := "SELECT COALESCE(json_agg(json_build_object('issuer_id',issuer_id,'crl_sha256',digest,'crl_number',number::text,'crl_pem',pem,'this_update',this_update,'next_update',next_update) ORDER BY issuer_id),'[]'::json) FROM (SELECT DISTINCT ON(issuer_id) issuer_id,digest,number,pem,this_update,next_update FROM pki_crls WHERE issuer_id IN (" + strings.Join(ids, ",") + ") ORDER BY issuer_id,number DESC) latest"
			backend, calls := lateNativeCommandContractBackend(t, store, map[string][]byte{lateRegistryContractCommand(e.Journal.Stack, query): raw})
			before := deploymentplan.Digest(e.Journal)
			paired := []map[string]managedLateCRLRecord{}
			if mode == "non-repair-paired" {
				paired = append(paired, map[string]managedLateCRLRecord{})
			}
			records, proofs, err := readManagedLateRegistryCRLs(t.Context(), cfg, e, backend, recovery, managedLateObserveACKReady, paired...)
			if (err == nil) != (mode == "valid") {
				t.Fatal(mode, err)
			}
			if mode == "valid" {
				if len(records) != 9 || len(proofs) != 9 {
					t.Fatal("not every native authority checked")
				}
				for _, row := range rows {
					if records[row.IssuerID] != row || proofs[row.IssuerID].Digest != row.Digest {
						t.Fatal("native signed row not preserved")
					}
				}
				command, err := os.ReadFile(calls)
				if err != nil || strings.Count(string(command), "registry-read") != 1 {
					t.Fatal("registry was not read once", err)
				}
			}
			if deploymentplan.Digest(e.Journal) != before {
				t.Fatal("read changed parent history")
			}
		})
	}
}

func TestManagedLateNativePostFinalizationExecutionContract(t *testing.T) {
	for _, mode := range []string{"valid", "idempotent", "changed-row", "outside-authority", "changed-tombstone", "files-not-erased", "changed-public-seed", "changed-pinned-tombstone"} {
		t.Run(mode, func(t *testing.T) {
			recovery, seed, state, subjects, _ := managedLateResidualNativeFixture(t)
			store, e, _, _ := freshStageFixture(t)
			cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack}}
			recovery.Ceremony.BootstrapCredentialNamespace = e.Journal.Stack + "-video-cloud"
			recovery.Ceremony.CustodyReader = "original-custody"
			late, remaining := recovery.ManagedLateSession, recovery.ManagedLateRemaining
			post := remaining.PostNativeCleanup
			native := post.Native
			native.CRLSHA256 = strings.Repeat("1", 64)
			native.CleanupSHA256 = strings.Repeat("2", 64)
			post.Native = native
			tombstone := managedLateCustodyTombstone{Schema: "rtk-cloud.bootstrap-credentials/v1", Finalization: native, Root: state.Root, Files: map[string]string{"bootstrap.key": late.OriginalKeySHA, "bootstrap.csr": late.OriginalCSRSHA, "bootstrap.crt": environmentPreflightBytesSHA256([]byte(seed.CertificatePEM))}}
			marker := mustProviderRecoveryJSON(tombstone)
			if mode == "idempotent" {
				post.TombstoneSHA = environmentPreflightBytesSHA256(marker)
			}
			query := "SELECT json_build_object('environment',environment,'session_id',session_id,'bootstrap_leaf_sha256',bootstrap_fingerprint,'crl_sha256',finalized_crl_sha256,'cleanup_sha256',cleanup_sha256,'finalized_at',finalized_at) FROM pki_deployment_bootstrap_sessions WHERE environment='dev' AND session_id='" + late.NewSessionID + "' AND finalized_at IS NOT NULL AND status IN ('sealed','expired')"
			prefix := []string{"--namespace", recovery.Ceremony.BootstrapCredentialNamespace, "exec", "deployment/" + recovery.Ceremony.CustodyReader, "--container", "custody", "--"}
			markerCommand := strings.Join(append(slices.Clone(prefix), "cat", "/run/pki-bootstrap/bootstrap.finalized.json"), " ")
			absentCommand := strings.Join(append(slices.Clone(prefix), "/bin/sh", "-ec", `test ! -e /run/pki-bootstrap/bootstrap.key && test ! -L /run/pki-bootstrap/bootstrap.key && test ! -e /run/pki-bootstrap/bootstrap.csr && test ! -L /run/pki-bootstrap/bootstrap.csr && test ! -e /run/pki-bootstrap/bootstrap.crt && test ! -L /run/pki-bootstrap/bootstrap.crt`), " ")
			switch mode {
			case "changed-row":
				native.LeafSHA256 = strings.Repeat("f", 64)
			case "outside-authority":
				native.FinalizedAt = remaining.TerminalAuthority.Deadline.Add(time.Second)
			case "changed-tombstone":
				tombstone.Files["bootstrap.key"] = strings.Repeat("f", 64)
				marker = mustProviderRecoveryJSON(tombstone)
			case "changed-public-seed":
				seed.CSRSHA = strings.Repeat("f", 64)
			case "changed-pinned-tombstone":
				post.TombstoneSHA = strings.Repeat("f", 64)
			}
			replies := map[string][]byte{lateRegistryContractCommand(e.Journal.Stack, query): mustProviderRecoveryJSON(native), markerCommand: marker, absentCommand: nil}
			if mode == "files-not-erased" {
				delete(replies, absentCommand)
			}
			backend, _ := lateNativeCommandContractBackend(t, store, replies)
			runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: backend, ServiceSessionRecovery: recovery}
			before := deploymentplan.Digest(e.Journal)
			err := observeManagedLatePostNativeFinalization(t.Context(), cfg, runtime, seed, state, subjects)
			if (err == nil) != (mode == "valid" || mode == "idempotent") {
				t.Fatal(mode, err)
			}
			if err == nil && (post.TombstoneSHA != environmentPreflightBytesSHA256(marker) || !managedLateFinalizationEqual(post.Native, native)) {
				t.Fatal("observed exact finalization not retained")
			}
			if deploymentplan.Digest(e.Journal) != before {
				t.Fatal("native observation changed original journal")
			}
		})
	}
}
