package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

const otaDeliveryTestURL = "https://cdn.example.invalid/artifacts"

func otaDeliveryTestFixture(cdn bool) ([]map[string]any, map[string]string) {
	selected := map[string]string{"CLOUD_STACK_NAME": "stack", "VIDEO_CLOUD_OTA_CDN_BASE_URL": "", "VIDEO_CLOUD_OTA_CDN_TOKEN_NAME": "__token__"}
	deployment := storageCutoverFixture("Deployment", otaServiceWorkloadName)
	container := storageCutoverMap(storageCutoverGet(deployment, "/spec/template/spec/containers").([]any)[0])
	container["name"] = "otaservice"
	env := container["env"].([]any)
	if cdn {
		selected["VIDEO_CLOUD_OTA_CDN_BASE_URL"] = otaDeliveryTestURL
	}
	env = append(env,
		map[string]any{"name": "VIDEO_CLOUD_OTA_CDN_BASE_URL", "value": selected["VIDEO_CLOUD_OTA_CDN_BASE_URL"]},
		map[string]any{"name": "VIDEO_CLOUD_OTA_CDN_TOKEN_NAME", "value": "__token__"},
	)
	if cdn {
		env = append(env, map[string]any{"name": "VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "ota-cdn-runtime", "key": "VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX"}}})
	}
	container["env"] = env
	owner := func(uid string) []any {
		return []any{map[string]any{"uid": uid, "controller": true}}
	}
	replicaSet := map[string]any{
		"apiVersion": "apps/v1", "kind": "ReplicaSet",
		"metadata": map[string]any{"name": "ota-revision", "namespace": "stack-video-cloud", "uid": "ota-revision-uid", "ownerReferences": owner(otaServiceWorkloadName + "-uid")},
		"spec":     map[string]any{"template": storageCutoverClone(storageCutoverGet(deployment, "/spec/template"))},
	}
	pod := map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "ota-pod", "namespace": "stack-video-cloud", "uid": "ota-pod-uid", "ownerReferences": owner("ota-revision-uid")},
		"spec":     storageCutoverClone(storageCutoverGet(deployment, "/spec/template/spec")),
		"status":   map[string]any{"phase": "Running"},
	}
	return []map[string]any{deployment, replicaSet, pod}, selected
}

func otaDeliveryTestContainer(object map[string]any) map[string]any {
	path := "/spec/template/spec/containers"
	if object["kind"] == "Pod" {
		path = "/spec/containers"
	}
	return storageCutoverMap(storageCutoverGet(object, path).([]any)[0])
}

func otaDeliveryTestEnv(object map[string]any, name string) map[string]any {
	for _, value := range otaDeliveryTestContainer(object)["env"].([]any) {
		entry := storageCutoverMap(value)
		if entry["name"] == name {
			return entry
		}
	}
	return nil
}

func otaDeliveryTestRemoveEnv(object map[string]any, name string) {
	container := otaDeliveryTestContainer(object)
	env := []any{}
	for _, value := range container["env"].([]any) {
		if storageCutoverMap(value)["name"] != name {
			env = append(env, value)
		}
	}
	container["env"] = env
}

func otaDeliveryTestBody(t *testing.T, items []map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func otaDeliveryTestMock(t *testing.T, key string) string {
	t.Helper()
	root := installStorageCutoverMock(t)
	// Missing mock Secrets must never fall through to an operator's kubectl.
	t.Setenv("STORAGE_KUBECTL_FALLBACK", "")
	if key != "" {
		storageCutoverMockWrite(t, root, map[string]any{
			"apiVersion": "v1", "kind": "Secret",
			"metadata": map[string]any{"namespace": "stack-video-cloud", "name": "ota-cdn-runtime"},
			"data":     map[string]any{"VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX": base64.StdEncoding.EncodeToString([]byte(key))},
		})
	}
	return root
}

func TestStorageReinitializationOTADeliveryAcceptsMatchingRuntime(t *testing.T) {
	for _, mode := range []string{"direct", "direct-omitted-empty", "direct-absent-defaults", "cdn", "cdn-default-token", "cdn-empty-token", "cdn-custom-token", "cdn-long-key"} {
		t.Run(mode, func(t *testing.T) {
			cdn := !strings.HasPrefix(mode, "direct")
			key := strings.Repeat("ab", 32)
			if !cdn {
				key = ""
			} else if mode == "cdn-long-key" {
				key = strings.Repeat("ab", 64)
			}
			otaDeliveryTestMock(t, key)
			items, selected := otaDeliveryTestFixture(cdn)
			if mode == "direct-omitted-empty" {
				for _, i := range []int{0, 2} {
					delete(otaDeliveryTestEnv(items[i], "VIDEO_CLOUD_OTA_CDN_BASE_URL"), "value")
					delete(otaDeliveryTestEnv(items[i], "VIDEO_CLOUD_OTA_CDN_TOKEN_NAME"), "value")
				}
			}
			if mode == "direct-absent-defaults" {
				for _, i := range []int{0, 2} {
					otaDeliveryTestRemoveEnv(items[i], "VIDEO_CLOUD_OTA_CDN_BASE_URL")
					otaDeliveryTestRemoveEnv(items[i], "VIDEO_CLOUD_OTA_CDN_TOKEN_NAME")
					otaDeliveryTestContainer(items[i])["envFrom"] = []any{}
				}
			}
			if mode == "cdn-default-token" {
				delete(selected, "VIDEO_CLOUD_OTA_CDN_TOKEN_NAME")
			}
			if mode == "cdn-empty-token" {
				selected["VIDEO_CLOUD_OTA_CDN_TOKEN_NAME"] = ""
				for _, i := range []int{0, 2} {
					otaDeliveryTestEnv(items[i], "VIDEO_CLOUD_OTA_CDN_TOKEN_NAME")["value"] = ""
				}
			}
			if mode == "cdn-custom-token" {
				selected["VIDEO_CLOUD_OTA_CDN_TOKEN_NAME"] = "download_auth"
				for _, i := range []int{0, 2} {
					otaDeliveryTestEnv(items[i], "VIDEO_CLOUD_OTA_CDN_TOKEN_NAME")["value"] = "download_auth"
				}
			}
			if err := validateStorageReinitializationOTADelivery(otaDeliveryTestBody(t, items), selected); err != nil {
				t.Fatalf("matching %s delivery rejected: %v", mode, err)
			}
		})
	}
}

func TestStorageReinitializationOTADeliveryRejectsWorkloadMismatch(t *testing.T) {
	for _, workload := range []struct {
		name  string
		index int
	}{{"deployment", 0}, {"pod", 2}} {
		for _, field := range []string{"VIDEO_CLOUD_OTA_CDN_BASE_URL", "VIDEO_CLOUD_OTA_CDN_TOKEN_NAME"} {
			t.Run(workload.name+"/"+field, func(t *testing.T) {
				otaDeliveryTestMock(t, strings.Repeat("ab", 32))
				items, selected := otaDeliveryTestFixture(true)
				otaDeliveryTestEnv(items[workload.index], field)["value"] = "different"
				if err := validateStorageReinitializationOTADelivery(otaDeliveryTestBody(t, items), selected); err == nil {
					t.Fatal("selected and existing OTA delivery mismatch accepted")
				}
			})
		}
	}
	for _, cdn := range []bool{false, true} {
		t.Run(map[bool]string{false: "selected-cdn-live-direct", true: "selected-direct-live-cdn"}[cdn], func(t *testing.T) {
			otaDeliveryTestMock(t, strings.Repeat("ab", 32))
			items, selected := otaDeliveryTestFixture(cdn)
			selected["VIDEO_CLOUD_OTA_CDN_BASE_URL"] = otaDeliveryTestURL
			if cdn {
				selected["VIDEO_CLOUD_OTA_CDN_BASE_URL"] = ""
			}
			if err := validateStorageReinitializationOTADelivery(otaDeliveryTestBody(t, items), selected); err == nil {
				t.Fatal("implicit delivery mode transition accepted")
			}
		})
	}
}

func TestStorageReinitializationOTADeliveryRequiresExistingWorkloadAndPod(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]map[string]any) []map[string]any
	}{
		{"missing-deployment", func(items []map[string]any) []map[string]any { return items[1:] }},
		{"duplicate-deployment", func(items []map[string]any) []map[string]any {
			return append(items, storageCutoverClone(items[0]).(map[string]any))
		}},
		{"wrong-deployment-namespace", func(items []map[string]any) []map[string]any {
			storageCutoverMap(items[0]["metadata"])["namespace"] = "other-video-cloud"
			return items
		}},
		{"wrong-deployment-name", func(items []map[string]any) []map[string]any {
			storageCutoverMap(items[0]["metadata"])["name"] = "api"
			return items
		}},
		{"wrong-pod-namespace", func(items []map[string]any) []map[string]any {
			storageCutoverMap(items[2]["metadata"])["namespace"] = "other-video-cloud"
			return items
		}},
		{"missing-pod", func(items []map[string]any) []map[string]any { return items[:2] }},
		{"only-succeeded-pod", func(items []map[string]any) []map[string]any {
			storageCutoverMap(items[2]["status"])["phase"] = "Succeeded"
			return items
		}},
		{"only-failed-pod", func(items []map[string]any) []map[string]any {
			storageCutoverMap(items[2]["status"])["phase"] = "Failed"
			return items
		}},
		{"unowned-pod", func(items []map[string]any) []map[string]any {
			delete(storageCutoverMap(items[2]["metadata"]), "ownerReferences")
			return items
		}},
	}
	for _, workload := range []struct {
		name  string
		index int
	}{{"deployment", 0}, {"pod", 2}} {
		cases = append(cases,
			struct {
				name   string
				mutate func([]map[string]any) []map[string]any
			}{workload.name + "-missing-app-container", func(items []map[string]any) []map[string]any {
				otaDeliveryTestContainer(items[workload.index])["name"] = "sidecar"
				return items
			}},
			struct {
				name   string
				mutate func([]map[string]any) []map[string]any
			}{workload.name + "-duplicate-app-container", func(items []map[string]any) []map[string]any {
				path := "/spec/template/spec"
				if workload.index == 2 {
					path = "/spec"
				}
				pod := storageCutoverMap(storageCutoverGet(items[workload.index], path))
				pod["containers"] = append(pod["containers"].([]any), storageCutoverClone(otaDeliveryTestContainer(items[workload.index])))
				return items
			}},
		)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			otaDeliveryTestMock(t, "")
			items, selected := otaDeliveryTestFixture(false)
			body := otaDeliveryTestBody(t, tc.mutate(items))
			if tc.name == "unowned-pod" {
				var err error
				body, err = storageReinitializationOTAInventory(body, "stack")
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := validateStorageReinitializationOTADelivery(body, selected); err == nil {
				t.Fatal("incomplete or ambiguous OTA workload inventory accepted")
			}
		})
	}
}

func TestStorageReinitializationOTADeliveryIgnoresTerminalPods(t *testing.T) {
	for _, phase := range []string{"Succeeded", "Failed"} {
		t.Run(phase, func(t *testing.T) {
			otaDeliveryTestMock(t, "")
			items, selected := otaDeliveryTestFixture(false)
			terminal := storageCutoverClone(items[2]).(map[string]any)
			storageCutoverMap(terminal["metadata"])["name"] = "previous-ota-pod"
			storageCutoverMap(terminal["metadata"])["uid"] = "previous-ota-pod-uid"
			storageCutoverMap(terminal["status"])["phase"] = phase
			otaDeliveryTestEnv(terminal, "VIDEO_CLOUD_OTA_CDN_BASE_URL")["value"] = otaDeliveryTestURL
			if err := validateStorageReinitializationOTADelivery(otaDeliveryTestBody(t, append(items, terminal)), selected); err != nil {
				t.Fatalf("terminal Pod changed active delivery selection: %v", err)
			}
		})
	}
}

func TestStorageReinitializationOTADeliveryRejectsAmbiguousEnvironment(t *testing.T) {
	for _, workload := range []struct {
		name  string
		index int
	}{{"deployment", 0}, {"pod", 2}} {
		for _, field := range []string{"VIDEO_CLOUD_OTA_CDN_BASE_URL", "VIDEO_CLOUD_OTA_CDN_TOKEN_NAME"} {
			for _, mutation := range []string{"missing", "duplicate", "value-from", "non-string"} {
				t.Run(workload.name+"/"+field+"/"+mutation, func(t *testing.T) {
					otaDeliveryTestMock(t, strings.Repeat("ab", 32))
					items, selected := otaDeliveryTestFixture(mutation == "missing")
					if mutation == "missing" && field == "VIDEO_CLOUD_OTA_CDN_TOKEN_NAME" {
						selected[field] = "download_auth"
						for _, i := range []int{0, 2} {
							otaDeliveryTestEnv(items[i], field)["value"] = "download_auth"
						}
					}
					object := items[workload.index]
					switch mutation {
					case "missing":
						otaDeliveryTestRemoveEnv(object, field)
					case "duplicate":
						container := otaDeliveryTestContainer(object)
						container["env"] = append(container["env"].([]any), storageCutoverClone(otaDeliveryTestEnv(object, field)))
					case "value-from":
						entry := otaDeliveryTestEnv(object, field)
						delete(entry, "value")
						entry["valueFrom"] = map[string]any{"configMapKeyRef": map[string]any{"name": "cdn-settings", "key": field}}
					case "non-string":
						otaDeliveryTestEnv(object, field)["value"] = float64(0)
					}
					if err := validateStorageReinitializationOTADelivery(otaDeliveryTestBody(t, items), selected); err == nil {
						t.Fatal("ambiguous CDN environment binding accepted")
					}
				})
			}
		}
		for _, from := range []string{"secret", "config-map", "unrelated-config-map"} {
			t.Run(workload.name+"/env-from/"+from, func(t *testing.T) {
				otaDeliveryTestMock(t, "")
				items, selected := otaDeliveryTestFixture(false)
				ref := "configMapRef"
				if from == "secret" {
					ref = "secretRef"
				}
				otaDeliveryTestContainer(items[workload.index])["envFrom"] = []any{map[string]any{ref: map[string]any{"name": from}}}
				if err := validateStorageReinitializationOTADelivery(otaDeliveryTestBody(t, items), selected); err == nil {
					t.Fatal("inherited environment accepted for a running OTA process")
				}
			})
		}
	}
	for _, field := range []string{"VIDEO_CLOUD_OTA_CDN_BASE_URL", "VIDEO_CLOUD_OTA_CDN_TOKEN_NAME"} {
		t.Run("expansion/"+field, func(t *testing.T) {
			otaDeliveryTestMock(t, strings.Repeat("ab", 32))
			items, selected := otaDeliveryTestFixture(true)
			value := "$(TOKEN_NAME)"
			if field == "VIDEO_CLOUD_OTA_CDN_BASE_URL" {
				value = "https://cdn.example.invalid/$(ARTIFACT_PREFIX)"
			}
			selected[field] = value
			for _, i := range []int{0, 2} {
				otaDeliveryTestEnv(items[i], field)["value"] = value
			}
			if err := validateStorageReinitializationOTADelivery(otaDeliveryTestBody(t, items), selected); err == nil {
				t.Fatal("environment expansion accepted as an exact delivery binding")
			}
		})
	}
}

func TestStorageReinitializationOTADeliveryRejectsInvalidKeyBinding(t *testing.T) {
	for _, workload := range []struct {
		name  string
		index int
	}{{"deployment", 0}, {"pod", 2}} {
		for _, mutation := range []string{"missing", "literal", "other-secret", "other-key", "optional", "config-map", "duplicate"} {
			t.Run(workload.name+"/"+mutation, func(t *testing.T) {
				otaDeliveryTestMock(t, strings.Repeat("ab", 32))
				items, selected := otaDeliveryTestFixture(true)
				object := items[workload.index]
				entry := otaDeliveryTestEnv(object, "VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX")
				ref := storageCutoverMap(storageCutoverMap(entry["valueFrom"])["secretKeyRef"])
				switch mutation {
				case "missing":
					otaDeliveryTestRemoveEnv(object, "VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX")
				case "literal":
					delete(entry, "valueFrom")
					entry["value"] = strings.Repeat("ab", 32)
				case "other-secret":
					ref["name"] = "other-cdn-key"
				case "other-key":
					ref["key"] = "OTHER_TOKEN_KEY"
				case "optional":
					ref["optional"] = true
				case "config-map":
					entry["valueFrom"] = map[string]any{"configMapKeyRef": ref}
				case "duplicate":
					container := otaDeliveryTestContainer(object)
					container["env"] = append(container["env"].([]any), storageCutoverClone(entry))
				}
				if err := validateStorageReinitializationOTADelivery(otaDeliveryTestBody(t, items), selected); err == nil {
					t.Fatal("invalid or ambiguous CDN token key binding accepted")
				}
			})
		}
		t.Run(workload.name+"/direct-with-key", func(t *testing.T) {
			otaDeliveryTestMock(t, strings.Repeat("ab", 32))
			items, selected := otaDeliveryTestFixture(false)
			cdnItems, _ := otaDeliveryTestFixture(true)
			container := otaDeliveryTestContainer(items[workload.index])
			container["env"] = append(container["env"].([]any), otaDeliveryTestEnv(cdnItems[workload.index], "VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX"))
			if err := validateStorageReinitializationOTADelivery(otaDeliveryTestBody(t, items), selected); err == nil {
				t.Fatal("direct delivery accepted a latent CDN key")
			}
		})
	}
}

func TestStorageReinitializationOTADeliveryRejectsInvalidRuntimeSecret(t *testing.T) {
	for _, tc := range []struct{ name, key string }{{"missing-secret", ""}, {"short-key", strings.Repeat("ab", 31)}, {"non-hex-key", strings.Repeat("zz", 32)}} {
		t.Run(tc.name, func(t *testing.T) {
			otaDeliveryTestMock(t, tc.key)
			items, selected := otaDeliveryTestFixture(true)
			if err := validateStorageReinitializationOTADelivery(otaDeliveryTestBody(t, items), selected); err == nil {
				t.Fatal("unusable CDN runtime Secret accepted")
			}
		})
	}
	for _, tc := range []struct {
		name string
		data map[string]any
	}{
		{"missing-key", map[string]any{}},
		{"empty-key", map[string]any{"VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX": ""}},
		{"invalid-base64", map[string]any{"VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX": "not base64"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := otaDeliveryTestMock(t, "")
			storageCutoverMockWrite(t, root, map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "ota-cdn-runtime", "namespace": "stack-video-cloud"}, "data": tc.data})
			items, selected := otaDeliveryTestFixture(true)
			if err := validateStorageReinitializationOTADelivery(otaDeliveryTestBody(t, items), selected); err == nil {
				t.Fatal("incomplete CDN runtime Secret accepted")
			}
		})
	}
}
