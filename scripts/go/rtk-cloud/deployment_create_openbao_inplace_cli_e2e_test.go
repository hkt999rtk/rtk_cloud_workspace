package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The same CLI qualification/execution body is exercised against the original
// isolated create journal and a file-backed API; all keys are test fixtures.
func TestOpenBaoInPlaceFormalScopedLifecycleInterrupted(t *testing.T) {
	for _, failure := range []string{"template", "patch"} {
		t.Run(failure, func(t *testing.T) {
			cfg, store, old, _, templatePlan := providerExecutionContractFixture(t)
			var canonicalKube []byte
			if failure == "template" {
				templateOperator, err := store.readOperator()
				if err != nil {
					t.Fatal(err)
				}
				raw, err := yaml.Marshal(frozenKubernetesFixture(t))
				if err != nil {
					t.Fatal(err)
				}
				checked, err := newEnvironmentKubernetesBackend(string(raw), "dev")
				if err != nil {
					t.Fatal(err)
				}
				canonicalKube = checked.KubeconfigContent
				cfg, store, old, _, _ = providerExecutionContractFixture(t, providerExecutionContractInputs{cfg: cfg, operator: templateOperator, bootstrap: templatePlan.Bootstrap, kubeconfig: canonicalKube})
			}
			publication := newCreateOpenBaoACKImageFixture(t)
			t.Setenv("RTK_CLOUD_CONFIG_ROOT", store.ConfigRoot)
			_, _, objects := existingFileOpenBaoFixture("dev", true)
			set := deploymentplan.Clone(objects[0])
			managedNamedField(environmentPKIPod(set)["containers"], "openbao")["readinessProbe"] = deploymentplan.Object{"exec": deploymentplan.Object{"command": []any{"/bin/sh", "-ec", "original-readiness-command"}}}
			meta := deploymentplan.Map(set["metadata"])
			meta["uid"] = "11111111-1111-4111-8111-111111111111"
			meta["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": cfg.Values["CLOUD_STACK_NAME"], "app.kubernetes.io/managed-by": "rtk-cloud"}
			oldRef := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("a", 64)
			managedNamedField(environmentPKIPod(set)["containers"], "openbao-pki")["image"] = oldRef
			managedNamedField(environmentPKIPod(set)["initContainers"], "openbao-pki-install")["image"] = oldRef
			target := createVersionImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("d", 64), SourceRevision: publication.selection.Revision, Publication: &createVersionPublication{Repository: "hkt999rtk/rtk_video_cloud", RunID: 77, Attempt: 2, ManifestSHA: publication.selection.ManifestSHA}}
			previous := map[string]string{"UNRELATED": "keep"}
			selection := map[string]any{"environment": "dev", "images": map[string]any{"LKE_OPENBAO_PKI_IMAGE": target}}
			selected, _ := json.Marshal(selection)
			current := map[string]string{"UNRELATED": "keep", "DEPLOYMENT_IMAGES": string(selected)}
			receipt, _ := json.Marshal(map[string]any{"previous_selection": "", "selection": selection, "differences": []any{map[string]any{"role": "LKE_OPENBAO_PKI_IMAGE", "current": oldRef, "latest": target, "run_url": "https://github.com/example"}}})
			if err := store.write("deployment/version-updates/test.json", receipt, false); err != nil {
				t.Fatal(err)
			}
			for k, v := range current {
				if err := store.write(filepath.Join("operator/env", k), []byte(v), false); err != nil {
					t.Fatal(err)
				}
			}
			parent := environmentCreatePlan{Environment: "dev", Stack: old.Journal.Stack, ConfigurationSHA256: deploymentplan.Digest(cfg), OperatorSHA256: deploymentplan.Digest(previous), Release: environmentRelease{Images: map[string]environmentReleaseImage{"LKE_OPENBAO_PKI_IMAGE": {Reference: oldRef}}}}
			e, _ := providerExecutionContractFork(t, old, parent, "isolated-openbao-cli")
			e.originalPlanSHA = deploymentplan.Digest(parent)
			e.versionState = &createOperatorVersionState{}
			_, image, err := verifyCreateEnvironmentOperator(t.Context(), cfg, store, e, current, &parent, publication.command(t))
			if err != nil {
				t.Fatal("environment image evidence", err)
			}
			selectionSHA, manifestSHA, ok := createOperatorVersionEvidence(e, current)
			if !ok {
				t.Fatal("missing exact selected image evidence")
			}
			sourceBackend := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{deploymentplan.ID(set): deploymentplan.Clone(set)}, writes: map[string]int{}}}
			defs := []deploymentplan.Definition{{Object: set, Phase: "workload"}}
			source, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: deploymentplan.Digest("source")}, defs, []deploymentplan.Object{set})
			if err != nil {
				t.Fatal(err)
			}
			qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
				return p.AdmitResumable(ctx, sourceBackend, store.Root)
			}
			fence := func(ctx context.Context, p *deploymentplan.Plan) error {
				return sourceBackend.CheckParentExecution(ctx, p, store.Root)
			}
			accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, sourceBackend) }
			if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "original-openbao", source, sourceBackend, qualify, fence, accept, func(context.Context) error { return nil }, io.Discard); err != nil {
				t.Fatal("source stage", err)
			}
			ns := e.Journal.Stack + "-secrets"
			podID := "Pod/" + ns + "/openbao-0"
			setID := deploymentplan.ID(set)
			pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "openbao-0", "namespace": ns, "uid": "original-pod", "resourceVersion": "3", "labels": deploymentplan.Object{"controller-revision-hash": "old-revision"}, "ownerReferences": []any{deploymentplan.Object{"controller": true, "kind": "StatefulSet", "name": "openbao", "uid": meta["uid"]}}}, "spec": deploymentplan.Clone(environmentPKIPod(set)), "status": deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}, "containerStatuses": []any{deploymentplan.Object{"name": "openbao", "ready": true, "containerID": "provider-id"}, deploymentplan.Object{"name": "openbao-pki", "ready": true, "containerID": "old-host", "imageID": oldRef}}, "initContainerStatuses": []any{deploymentplan.Object{"name": "openbao-pki-install", "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0)}}}}}}
			addOpenBaoFixtureClaimVolumes(set, pod)
			native, dir, calls := unifiedSessionNativeBackend(t, store, map[string]deploymentplan.Object{setID: set, podID: pod}, nil)
			native.RolloutTimeout = time.Second
			installOpenBaoScopedFakePatch(t, dir)
			if len(canonicalKube) != 0 {
				protocolPath := filepath.Join(dir, "unified-kubectl.py")
				protocol, err := os.ReadFile(protocolPath)
				if err != nil {
					t.Fatal(err)
				}
				original := "if f.read() != b'isolated-native-kubeconfig': sys.exit(72)"
				replacement := fmt.Sprintf("if f.read() not in (b'isolated-native-kubeconfig', bytes.fromhex('%x')): sys.exit(72)", canonicalKube)
				if !strings.Contains(string(protocol), original) {
					t.Fatal("known synthetic Kubernetes input fence unavailable")
				}
				if err := os.WriteFile(protocolPath, []byte(strings.Replace(string(protocol), original, replacement, 1)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: e, cfg: cfg, store: store}
			prepare := func(ctx context.Context, env string) (*openBaoInPlacePrepared, error) {
				if env != "dev" {
					return nil, fmt.Errorf("wrong environment")
				}
				_, actual, err := readEnvironmentCreateOperator(cfg)
				if err != nil {
					return nil, err
				}
				_, verified, err := verifyCreateEnvironmentOperator(ctx, cfg, store, e, actual, &parent, publication.command(t))
				if err != nil {
					return nil, err
				}
				s, m, ok := createOperatorVersionEvidence(e, actual)
				if !ok || s != selectionSHA || m != manifestSHA || verified.Reference != image.Reference {
					return nil, fmt.Errorf("selection changed")
				}
				return &openBaoInPlacePrepared{cfg: cfg, store: store, execution: e, image: verified, selection: s, manifest: m, native: native, backend: backend, cleanup: func() {}}, nil
			}
			if err := runCreateOpenBaoInPlaceWith([]string{"--environment", "dev"}, prepare); err != nil {
				t.Fatal("scoped preflight", err)
			}
			files, err := filepath.Glob(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "openbao-inplace", "*.json"))
			if err != nil || len(files) != 1 {
				t.Fatal("qualified plan missing", err)
			}
			review := strings.TrimSuffix(filepath.Base(files[0]), ".json")
			if _, err := createHistoricalReleaseSelection(t.Context(), cfg, store, e, parent); err == nil {
				t.Fatal("uncompleted host update authorized historical release handoff")
			}
			args := []string{"--environment", "dev", "--execute", "--plan-sha256", review, "--timeout", "30s"}
			interruptedArgs := append([]string(nil), args...)
			if failure == "template" {
				interruptedArgs[len(interruptedArgs)-1] = "3s"
			}
			marker := filepath.Join(dir, "fail-"+failure)
			if err := os.WriteFile(marker, []byte("1"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := runCreateOpenBaoInPlaceWith(interruptedArgs, prepare); err == nil {
				t.Fatal("interruption not observed")
			}
			if failure == "template" {
				e.versionState = &createOperatorVersionState{}
				ciReads := 0
				_, _, err := verifyCreateEnvironmentOperator(t.Context(), cfg, store, e, current, &parent, func(context.Context, string, ...string) ([]byte, error) {
					ciReads++
					return nil, errors.New("isolated incomplete publication unavailable")
				})
				if err == nil || ciReads == 0 {
					t.Fatal("incomplete handoff bypassed formal publication")
				}
			}
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			if failure == "template" {
				openBaoFixtureReleaseTemplate(t, dir, setID)
			}
			if err := runCreateOpenBaoInPlaceWith(args, prepare); err != nil {
				openBaoFixtureDiagnose(t, store, e, cfg, native, files[0], parent, current, publication)
				raw, _ := os.ReadFile(calls)
				t.Fatalf("scoped resume: %v calls=%s", err, raw)
			}
			owner, err := verifyOpenBaoInPlaceReady(t.Context(), cfg, store, e, native)
			if err != nil || owner.PodUID != "original-pod" || owner.ContainerID != "new-host" {
				t.Fatal("missing real accepted owner", err)
			}
			entry := createOpenBaoACKCompletionEntry(e.Journal, "openbao-host-inplace-"+review)
			if entry == nil || entry.State != "completed" {
				t.Fatal("missing accepted producer receipt")
			}
			historical, err := createHistoricalReleaseSelection(t.Context(), cfg, store, e, parent)
			if err != nil || historical.Images["LKE_OPENBAO_PKI_IMAGE"].Reference != oldRef {
				t.Fatal("completed host successor did not authorize original release evidence", err)
			}
			unchanged, err := readEnvironmentImageSelection(store)
			if err != nil || unchanged.Images["LKE_OPENBAO_PKI_IMAGE"].Reference != target.Reference {
				t.Fatal("historical qualification changed selected runtime image", err)
			}
			stableReceipt := deploymentplan.Digest(entry.Receipt)
			if err = runCreateOpenBaoInPlaceWith(args, prepare); err != nil {
				t.Fatal("completed producer retry", err)
			}
			entry = createOpenBaoACKCompletionEntry(e.Journal, "openbao-host-inplace-"+review)
			if deploymentplan.Digest(entry.Receipt) != stableReceipt {
				t.Fatal("completed receipt was rewritten")
			}
			if failure == "template" {
				// A cold process must reuse this exact completed publication while
				// Root startup is unavailable; runtime readiness remains separate.
				ciCalls := 0
				unavailableCI := func(context.Context, string, ...string) ([]byte, error) {
					ciCalls++
					return nil, errors.New("isolated pinned publication unavailable")
				}
				podFile := filepath.Join(dir, strings.ReplaceAll(podID, "/", "_")+".json")
				podBytes, err := os.ReadFile(podFile)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(podFile); err != nil {
					t.Fatal(err)
				}
				e.versionState = &createOperatorVersionState{}
				restored, reused, err := verifyCreateEnvironmentOperator(t.Context(), cfg, store, e, current, &parent, unavailableCI)
				if err != nil || reused.Reference != target.Reference || reused.SourceRevision != target.SourceRevision || reused.Publication == nil || !sameCreateVersionJSON(reused.Publication, target.Publication) || deploymentplan.Digest(restored) != parent.OperatorSHA256 || ciCalls != 0 {
					t.Fatal("cold completed publication required CI or Root readiness", err, ciCalls)
				}
				if err := os.WriteFile(podFile, podBytes, 0600); err != nil {
					t.Fatal(err)
				}
				for _, mode := range []string{"missing-qualification", "native-uid", "current-selection"} {
					t.Run("cold-"+mode, func(t *testing.T) {
						e.versionState = &createOperatorVersionState{}
						actual := current
						qualificationBytes, err := os.ReadFile(files[0])
						if err != nil {
							t.Fatal(err)
						}
						setFile := filepath.Join(dir, strings.ReplaceAll(setID, "/", "_")+".json")
						setBytes, err := os.ReadFile(setFile)
						if err != nil {
							t.Fatal(err)
						}
						switch mode {
						case "missing-qualification":
							if err := os.Remove(files[0]); err != nil {
								t.Fatal(err)
							}
							defer os.WriteFile(files[0], qualificationBytes, 0600)
						case "native-uid":
							var replaced deploymentplan.Object
							if err := json.Unmarshal(setBytes, &replaced); err != nil {
								t.Fatal(err)
							}
							deploymentplan.Map(replaced["metadata"])["uid"] = "foreign-native-owner"
							if err := os.WriteFile(setFile, mustProviderRecoveryJSON(replaced), 0600); err != nil {
								t.Fatal(err)
							}
							defer os.WriteFile(setFile, setBytes, 0600)
						case "current-selection":
							actual = map[string]string{}
							for k, v := range current {
								actual[k] = v
							}
							actual["UNRELATED"] = "foreign"
						}
						if _, _, err := verifyCreateEnvironmentOperator(t.Context(), cfg, store, e, actual, &parent, unavailableCI); err == nil {
							t.Fatal("cold publication accepted changed authority", mode)
						}
					})
				}
				e.versionState = &createOperatorVersionState{}
			}

			originalQualification, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			var changed map[string]any
			if err = json.Unmarshal(originalQualification, &changed); err != nil {
				t.Fatal(err)
			}
			changed["TargetImage"] = oldRef
			rawChanged, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(files[0], rawChanged, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = verifyOpenBaoInPlaceReady(t.Context(), cfg, store, e, native); err == nil {
				t.Fatal("changed qualification was accepted")
			}
			if err = os.WriteFile(files[0], originalQualification, 0600); err != nil {
				t.Fatal(err)
			}
			currentSet, err := native.Read(t.Context(), setID)
			if err != nil {
				t.Fatal(err)
			}
			currentPod, err := native.Read(t.Context(), podID)
			if err != nil {
				t.Fatal(err)
			}
			want := deploymentplan.Clone(set)
			managedNamedField(environmentPKIPod(want)["containers"], "openbao-pki")["image"] = target.Reference
			managedNamedField(environmentPKIPod(want)["initContainers"], "openbao-pki-install")["image"] = target.Reference
			definitions := []deploymentplan.Definition{{Object: want, Phase: "workload"}}
			upgrade, err := deploymentplan.Compile(source.Inputs, definitions, []deploymentplan.Object{currentSet, currentPod})
			if err != nil || len(upgrade.Resources) != 1 || upgrade.Resources[0].Action != "retain" {
				t.Fatal("normal target did not preserve updated original owner", err)
			}
			if err = qualifyEnvironmentOnDeleteOpenBaoPlan(upgrade, definitions, []deploymentplan.Object{currentSet, currentPod}, owner); err != nil || upgrade.Resources[0].Action != "retain" {
				t.Fatal("recorded OnDelete successor was not accepted", err)
			}
			raw, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(string(raw), "\n")
			patches := 0
			for _, line := range lines {
				if strings.Contains(line, " patch pod ") && !strings.Contains(line, "--dry-run=server") {
					patches++
				}
			}
			if patches != 1 {
				t.Fatalf("sidecar mutated %d times", patches)
			}

			if failure == "template" {
				// Reproduce a cold publication read after the real original
				// workload has written a same-UID template, then failed readiness.
				currentSet, err := native.Read(t.Context(), setID)
				if err != nil {
					t.Fatal(err)
				}
				changed := deploymentplan.Clone(currentSet)
				managedNamedField(environmentPKIPod(changed)["containers"], "openbao")["readinessProbe"] = deploymentplan.Object{"exec": deploymentplan.Object{"command": []any{"/bin/sh", "-ec", "test -s /run/original/readiness"}}}
				route := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": ns, "name": "unstarted-tail"}, "data": deploymentplan.Object{"unchanged": "routing"}}
				defs := []deploymentplan.Definition{{Object: changed, Phase: "workload"}, {Object: route, Phase: "routing"}}
				inputs := source.Inputs
				inputs.BindingsSHA256 = environmentCreateStageBindingsSHA("workload", defs)
				later, err := deploymentplan.Compile(inputs, defs, []deploymentplan.Object{currentSet})
				if err != nil {
					t.Fatal(err)
				}
				laterBackend := openBaoLaterWorkloadBackend{freshStageBackend: freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{setID: currentSet}, writes: map[string]int{}, failure: "after-write-workload"}}, UID: "99999999-9999-4999-8999-999999999999"}
				if err = executeEnvironmentCreateStagePlan(t.Context(), store, e, "workload", later, laterBackend, fence, fence, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, laterBackend) }, func(context.Context) error { return nil }, io.Discard); err == nil {
					t.Fatal("original workload did not interrupt")
				}

				progress, exists, readErr := later.ReadExecution(store.Root)
				if readErr != nil || !exists || len(progress.Completed) != 1 || laterBackend.writes[setID] != 1 {
					t.Fatalf("real later write not reached: execute=%v read=%v exists=%v completed=%d writes=%d", err, readErr, exists, len(progress.Completed), laterBackend.writes[setID])
				}
				setFile := filepath.Join(dir, strings.ReplaceAll(setID, "/", "_")+".json")
				if err = os.WriteFile(setFile, mustProviderRecoveryJSON(laterBackend.objects[setID]), 0600); err != nil {
					t.Fatal(err)
				}
				e.versionState = &createOperatorVersionState{}
				_, actual, err := verifyCreateEnvironmentOperator(t.Context(), cfg, store, e, current, &parent, func(context.Context, string, ...string) ([]byte, error) {
					return nil, errors.New("completed exact publication must not need CI")
				})
				if err != nil || actual.Reference != target.Reference {
					t.Fatal("recorded workload invalidated completed OpenBao publication", err)
				}
				if _, err := verifyOpenBaoInPlaceReady(t.Context(), cfg, store, e, native); err != nil {
					t.Fatal("recorded template invalidated original OnDelete Pod", err)
				}
			}
		})
	}
}

func installOpenBaoScopedFakePatch(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "unified-kubectl.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	// Kubernetes assigns canonical UUIDs; the generic read-only fixture uses
	// synthetic non-UUID identifiers and cannot represent execution leases.
	if !strings.Contains(script, "meta['uid'] = 'fixture-' + uuid.uuid4().hex") {
		t.Fatal("isolated kube UID hook changed")
	}
	script = strings.Replace(script, "meta['uid'] = 'fixture-' + uuid.uuid4().hex", "meta['uid'] = str(uuid.uuid4())", 1)
	marker := "    if a[0] == 'get':"
	insert := `    if len(a) >= 7 and a[0] == '--namespace' and a[2:4] == ['patch','pod']:
        ns, name = a[1], a[4]
        file = path('Pod',ns,name); obj=load(file)
        if obj is None: fail('NotFound')
        arg = next((x for x in a if x.startswith('--patch=')),None)
        if arg is None: fail('missing patch')
        patch=json.loads(arg[len('--patch='):])
        def locate(pointer):
            cur=obj
            parts=pointer.strip('/').split('/')
            for part in parts[:-1]: cur=cur[int(part)] if isinstance(cur,list) else cur[part]
            return cur,parts[-1]
        for item in patch:
            target,key=locate(item['path']); old=target[int(key)] if isinstance(target,list) else target.get(key)
            if item['op']=='test' and old!=item['value']: fail('Conflict')
            if item['op']=='replace':
                if isinstance(target,list): target[int(key)]=item['value']
                else: target[key]=item['value']
        if '--dry-run=server' not in a:
            obj['metadata']['resourceVersion']='pod-after-patch'
            host=next(x for x in obj['status']['containerStatuses'] if x['name']=='openbao-pki')
            host['containerID']='new-host';host['imageID']=obj['spec']['containers'][1]['image'];host['ready']=True
            save(file,obj)
            if os.path.exists(os.path.join(root,'fail-patch')): fail('synthetic interruption after guarded patch')
        print(json.dumps(obj))
    elif a[0] == 'get':`
	if strings.Count(script, marker) != 1 {
		t.Fatal("isolated kube get hook changed")
	}
	script = strings.Replace(script, marker, insert, 1)
	marker = "        if '--dry-run=server' not in a: save(file, obj)"
	insert = `        if kind == 'StatefulSet' and name == 'openbao':
            meta['generation'] = 2
            obj['status'] = {'observedGeneration': 1 if os.path.exists(os.path.join(root,'fail-template')) else 2, 'updateRevision':'new-revision'}
        if '--dry-run=server' not in a: save(file, obj)`
	if strings.Count(script, marker) != 1 {
		t.Fatal("isolated kube replace hook changed")
	}
	script = strings.Replace(script, marker, insert, 1)
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
}

func openBaoFixtureReleaseTemplate(t *testing.T, dir, id string) {
	t.Helper()
	path := filepath.Join(dir, strings.ReplaceAll(id, "/", "_")+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var set deploymentplan.Object
	if err = json.Unmarshal(raw, &set); err != nil {
		t.Fatal(err)
	}
	deploymentplan.Map(set["status"])["observedGeneration"] = deploymentplan.Map(set["metadata"])["generation"]
	raw, err = json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func openBaoFixtureDiagnose(t *testing.T, store secretStore, e *environmentCreateExecution, cfg deploymentConfig, native environmentKubernetesBackend, path string, parent environmentCreatePlan, current map[string]string, publication *createOpenBaoACKImageFixture) {
	t.Helper()
	var q openBaoInPlaceQualification
	if err := readExecutionJSON(path, &q); err != nil {
		t.Log("qualification", err)
		return
	}
	t.Log("parent extends", partialSessionJournalExtends(q.ParentJournal, e.Journal), "fence", e.Check(t.Context()))
	entry := createOpenBaoACKCompletionEntry(e.Journal, "openbao-host-inplace-"+deploymentplan.Digest(q))
	if entry == nil {
		t.Log("handoff parent missing")
	} else {
		var recorded openBaoInPlaceReceipt
		decodeErr := decodeEnvironmentJSON(entry.Receipt, &recorded)
		loaded, loadErr := readOpenBaoInPlaceQualification(store, e, recorded.QualificationSHA256)
		intent, err := environmentCreatePlanSHA256(openBaoInPlaceIntent(loaded))
		t.Log("handoff state", entry.State, "decode", decodeErr, "qualification", loadErr, "intent", err == nil && intent == entry.IntentSHA256, "review stable", recorded.QualificationSHA256 == deploymentplan.Digest(q), "completedAt", !entry.CompletedAt.IsZero())
	}
	probe := openBaoInPlaceReceipt{QualificationSHA256: deploymentplan.Digest(q), TemplateStage: q.TemplateStage, TemplatePlanSHA: q.TemplatePlanSHA, HostContainerID: "new-host"}
	_, receiptErr := verifyOpenBaoInPlaceReceipt(t.Context(), cfg, store, e, native, probe)
	t.Log("direct receipt", receiptErr)
	opStore, operator, err := readEnvironmentCreateOperator(cfg)
	t.Log("operator read", err, "same store", opStore == store, "operator same", deploymentplan.Digest(operator) == deploymentplan.Digest(current))
	original, err := authenticatedCreateOperatorPlan(store, e)
	t.Log("original plan", err, "equal", original != nil && deploymentplan.Digest(*original) == deploymentplan.Digest(parent))
	_, img, err := verifyCreateEnvironmentOperator(t.Context(), cfg, store, e, operator, &parent, publication.command(t))
	a, b, ok := createOperatorVersionEvidence(e, operator)
	t.Log("selected image", err, img.Reference == q.TargetImage, "selection", ok && a == q.SelectionSHA, "manifest", b == q.ManifestSHA)
	source, originalSet, err := openBaoInPlaceSource(store, e, q)
	t.Log("source", err, source != nil)
	child, j, err := acceptedCreateStage(store, e, "kubernetes/"+q.TemplateStage)
	t.Log("child", err, child != nil, "owners", j.Owners)
	set, err := native.Read(t.Context(), "StatefulSet/"+e.Journal.Stack+"-secrets/openbao")
	t.Log("set read", err, "matches", set != nil && child != nil && deploymentplan.Matches(child.Resources[0].Desired, set))
	pod, err := native.Read(t.Context(), "Pod/"+e.Journal.Stack+"-secrets/openbao-0")
	t.Log("pod read", err)
	if originalSet != nil && set != nil && pod != nil {
		_, err := validateOpenBaoInPlaceObservation(q, originalSet, set, pod, "new-host")
		t.Log("observation", err)
	}
}

type openBaoLaterWorkloadBackend struct {
	freshStageBackend
	UID string
}
type openBaoLaterWorkloadLease struct {
	deploymentplan.ExecutionLease
	UID string
}

func (b openBaoLaterWorkloadBackend) AcquireExecution(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.ExecutionLease, error) {
	l, err := b.freshStageBackend.AcquireExecution(ctx, p)
	if err != nil {
		return nil, err
	}
	return openBaoLaterWorkloadLease{l, b.UID}, nil
}
func (l openBaoLaterWorkloadLease) Owner() deploymentplan.LeaseOwner {
	o := l.ExecutionLease.Owner()
	o.UID = l.UID
	return o
}
