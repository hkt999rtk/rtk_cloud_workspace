// Package deploymentplan compiles and fences an environment's desired resources.
// It has no environment-name branches, credential generation or cloud mutations.
package deploymentplan

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

const Version = 2

type Object = map[string]any

type Inputs struct {
	Environment         string `json:"environment"`
	Stack               string `json:"stack"`
	Operation           string `json:"operation"`
	ConfigurationSHA256 string `json:"configuration_sha256"`
	ReleaseSHA256       string `json:"release_sha256"`
	BindingsSHA256      string `json:"bindings_sha256"`
	StorageSHA256       string `json:"storage_sha256,omitempty"`
	CreateParentSHA256  string `json:"create_parent_sha256,omitempty"`
	CreateHandoffSHA256 string `json:"create_handoff_sha256,omitempty"`
}

type Resource struct {
	ID           string   `json:"id"`
	Phase        string   `json:"phase"`
	Action       string   `json:"action"`
	UID          string   `json:"uid,omitempty"`
	Desired      Object   `json:"desired"`
	Baseline     Object   `json:"baseline,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
}

type Plan struct {
	Version int `json:"schema_version"`
	Inputs
	Resources []Resource `json:"resources"`
}

// Definition is renderer output, never a live object used as a template.
type Definition struct {
	Object       Object
	Phase        string
	Dependencies []string
	Retain       bool
	Retire       bool
}

type Blocker struct{ ID, Resource, Cause, Next string }

func (b *Blocker) Error() string {
	return fmt.Sprintf("%s %s: %s; next: %s", b.ID, b.Resource, b.Cause, b.Next)
}
func blocked(id, resource, cause, next string) error { return &Blocker{id, resource, cause, next} }

func Map(v any) Object    { m, _ := v.(map[string]any); return m }
func String(v any) string { s, _ := v.(string); return s }
func Clone(o Object) Object {
	b, _ := json.Marshal(o)
	var c Object
	_ = json.Unmarshal(b, &c)
	return c
}
func Digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ID(o Object) string {
	m := Map(o["metadata"])
	return String(o["kind"]) + "/" + String(m["namespace"]) + "/" + String(m["name"])
}

var phases = map[string]int{"namespace": 0, "storage": 1, "bootstrap": 2, "configuration": 3, "migration": 4, "workload": 5, "routing": 6, "retirement": 7}
var kinds = map[string]bool{"Namespace": true, "Deployment": true, "StatefulSet": true, "DaemonSet": true, "Job": true, "CronJob": true, "Service": true, "ConfigMap": true, "Secret": true, "PersistentVolumeClaim": true, "ServiceAccount": true, "Role": true, "RoleBinding": true, "ClusterRole": true, "ClusterRoleBinding": true, "NetworkPolicy": true, "Ingress": true, "IngressClass": true, "ValidatingWebhookConfiguration": true, "PodDisruptionBudget": true, "HorizontalPodAutoscaler": true}

func controller(kind string) bool {
	return kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet" || kind == "CronJob" || kind == "Job" || kind == "Pod" || kind == "ReplicaSet"
}

func Compile(in Inputs, definitions []Definition, live []Object, adoptions ...map[string]string) (*Plan, error) {
	if len(adoptions) > 1 {
		return nil, errors.New("one explicit ownership adoption map is allowed")
	}
	if in.Environment == "" || in.Stack != "video-cloud-"+in.Environment || (in.Operation != "create" && in.Operation != "upgrade" && in.Operation != "scoped") || in.ConfigurationSHA256 == "" || in.ReleaseSHA256 == "" || in.BindingsSHA256 == "" {
		return nil, errors.New("complete environment, stack, operation and input digests required")
	}
	if len(definitions) == 0 {
		return nil, errors.New("complete desired resource inventory required")
	}
	byID := map[string]Object{}
	for _, object := range live {
		id := ID(object)
		if byID[id] != nil {
			return nil, errors.New("duplicate live resource")
		}
		byID[id] = object
	}
	p := &Plan{Version: Version, Inputs: in}
	seen := map[string]bool{}
	for _, definition := range definitions {
		desired := Clone(definition.Object)
		normalizeDeploymentRevision(desired)
		id := ID(desired)
		kind := String(desired["kind"])
		if kind == "Secret" {
			// stringData is write-only and does not exist in API readback. Compile
			// to the actual persisted data representation before admission/digest.
			if values, exists := desired["stringData"]; exists {
				plain, ok := values.(map[string]any)
				if !ok {
					return nil, errors.New("Secret stringData requires string values")
				}
				data := Map(desired["data"])
				if data == nil {
					data = Object{}
				}
				for key, value := range plain {
					text, ok := value.(string)
					if !ok {
						return nil, errors.New("Secret stringData requires string values")
					}
					data[key] = base64.StdEncoding.EncodeToString([]byte(text))
				}
				desired["data"] = data
				delete(desired, "stringData")
			}
		}
		metadata := Map(desired["metadata"])
		expectedUID := String(metadata["uid"])
		namespace, name := String(metadata["namespace"]), String(metadata["name"])
		clusterScoped := kind == "Namespace" || kind == "ClusterRole" || kind == "ClusterRoleBinding" || kind == "IngressClass" || kind == "ValidatingWebhookConfiguration"
		originalAdmission := originalAdmissionOwner(desired, byID[id], in.Stack, adoptions)
		if (!kinds[kind] && !(kind == "Pod" && in.Operation == "scoped")) || name == "" || (clusterScoped && (namespace != "" || !strings.HasPrefix(name, in.Stack+"-") && !originalAdmission)) || (!clusterScoped && !strings.HasPrefix(namespace, in.Stack+"-")) || seen[id] {
			return nil, blocked("deployment.inventory", id, "invalid, duplicate or foreign desired resource", "correct the shared renderer and selected environment intent")
		}
		if _, ok := phases[definition.Phase]; !ok {
			return nil, errors.New("unknown deployment phase")
		}
		seen[id] = true
		if definition.Retire && (definition.Retain || !retirable(kind) || definition.Phase != "retirement") {
			return nil, blocked("deployment.retirement", id, "resource requires a separate data or PKI lifecycle", "ordinary retirement is limited to explicitly reviewed workload, service, route and access owners")
		}
		if !definition.Retain {
			labels := Map(metadata["labels"])
			if labels == nil {
				labels = Object{}
				metadata["labels"] = labels
			}
			labels["rtk.realtek.com/stack"] = in.Stack
			labels["app.kubernetes.io/managed-by"] = "rtk-cloud"
		}
		delete(desired, "status")
		for _, key := range []string{"uid", "resourceVersion", "generation", "creationTimestamp", "managedFields"} {
			delete(metadata, key)
		}
		r := Resource{ID: id, Phase: definition.Phase, Action: "create", Desired: desired, Dependencies: definition.Dependencies}
		if current := byID[id]; current != nil {
			m := Map(current["metadata"])
			owner := String(Map(m["labels"])["rtk.realtek.com/stack"])
			adopted := false
			if len(adoptions) == 1 {
				adopted = adoptions[0][id] != "" && adoptions[0][id] == String(m["uid"]) && owner == ""
			}
			if String(m["uid"]) == "" || m["deletionTimestamp"] != nil || (owner != in.Stack && !adopted) {
				return nil, blocked("deployment.ownership", id, "live owner is unbound, foreign or terminating", "record and verify an explicit environment ownership adoption before deployment")
			}
			// An explicitly selected original owner cannot be rebound by a
			// fresh planning inventory to a replacement with the same name.
			if expectedUID != "" && String(m["uid"]) != expectedUID {
				return nil, blocked("deployment.ownership", id, "reviewed owner was replaced", "review the replacement through its own lifecycle")
			}
			if definition.Retire && expectedUID != "" {
				if !Matches(desired, current) {
					return nil, blocked("deployment.retirement", id, "reviewed retirement recipe changed", "authenticate the original owner and recipe before retirement")
				}
			}
			r.UID = String(m["uid"])
			r.Baseline = Projection(current)
			r.Action = "update"
			if !definition.Retire && !definition.Retain {
				if err := retainAllocatedFields(desired, current); err != nil {
					return nil, err
				}
			}
			if !definition.Retain && !definition.Retire {
				if err := validateLifecycle(id, desired, current); err != nil {
					return nil, err
				}
			}
			if definition.Retain || Matches(desired, current) {
				r.Action = "retain"
			}
			if definition.Retain && !Matches(desired, current) {
				return nil, blocked("deployment.lifecycle", id, "retained dependency differs from desired intent", "complete its explicitly reviewed lifecycle migration")
			}
			if definition.Retire {
				r.Action = "retire"
			}
		} else if expectedUID != "" {
			return nil, blocked("deployment.ownership", id, "reviewed original owner is absent", "reconcile the original owner; do not create a replacement")
		} else if definition.Retain {
			return nil, blocked("deployment.dependency", id, "required retained dependency is absent", "complete environment bootstrap or restore the exact managed owner")
		} else if definition.Retire {
			return nil, blocked("deployment.retirement", id, "reviewed retirement owner is absent", "archive the completed retirement record and repeat planning")
		}
		p.Resources = append(p.Resources, r)
	}
	for id, object := range byID {
		m := Map(object["metadata"])
		if controller(String(object["kind"])) && strings.HasPrefix(String(m["namespace"]), in.Stack+"-") && !seen[id] && in.Operation != "scoped" {
			if HistoricalTerminalWorkload(id, byID, in.Stack) || verifiedControllerChild(id, byID, seen, in.Stack, map[string]bool{}) {
				continue
			}
			return nil, blocked("deployment.inventory", id, "live controller has no declared renderer or retirement action", "add its shared component definition or review its retirement lifecycle")
		}
	}
	sort.Slice(p.Resources, func(i, j int) bool {
		a, b := p.Resources[i], p.Resources[j]
		if phases[a.Phase] != phases[b.Phase] {
			return phases[a.Phase] < phases[b.Phase]
		}
		return a.ID < b.ID
	})
	// Deterministic topological ordering inside each stage permits a resource to
	// depend on another whose name sorts later without weakening stage fences.
	remaining, ordered := p.Resources, []Resource{}
	completed := map[string]bool{}
	for len(remaining) > 0 {
		selected := -1
		for i, r := range remaining {
			if phases[r.Phase] != phases[remaining[0].Phase] {
				break
			}
			ready := true
			for _, dependency := range r.Dependencies {
				if !completed[dependency] {
					ready = false
					break
				}
			}
			if ready {
				selected = i
				break
			}
		}
		if selected < 0 {
			return nil, blocked("deployment.dependencies", remaining[0].ID, "dependency is absent, cyclic or in a later stage", "correct the shared dependency ordering")
		}
		r := remaining[selected]
		ordered = append(ordered, r)
		completed[r.ID] = true
		remaining = append(remaining[:selected], remaining[selected+1:]...)
	}
	p.Resources = ordered
	return p, nil
}

// Generated Pods, ReplicaSets and CronJob Jobs belong to a declared controller,
// not an extra component. Every edge must identify the exact live controller
// UID; labels, names alone and cycles cannot turn an unknown workload into one.
func verifiedControllerChild(id string, live map[string]Object, declared map[string]bool, stack string, visiting map[string]bool) bool {
	if visiting[id] || len(visiting) > 8 {
		return false
	}
	object := live[id]
	if object == nil {
		return false
	}
	meta := Map(object["metadata"])
	if String(meta["uid"]) == "" || !strings.HasPrefix(String(meta["namespace"]), stack+"-") {
		return false
	}
	owner := String(Map(meta["labels"])["rtk.realtek.com/stack"])
	if owner != "" && owner != stack {
		return false
	}
	if declared[id] {
		return true
	}
	kind := String(object["kind"])
	if kind != "Pod" && kind != "ReplicaSet" && kind != "Job" {
		return false
	}
	visiting[id] = true
	defer delete(visiting, id)
	refs, _ := meta["ownerReferences"].([]any)
	var ref Object
	for _, value := range refs {
		candidate := Map(value)
		if candidate["controller"] == true {
			if ref != nil {
				return false
			}
			ref = candidate
		}
	}
	if ref == nil {
		return false
	}
	parentKind := String(ref["kind"])
	allowed := kind == "ReplicaSet" && parentKind == "Deployment" || kind == "Job" && parentKind == "CronJob" || kind == "Pod" && (parentKind == "ReplicaSet" || parentKind == "StatefulSet" || parentKind == "DaemonSet" || parentKind == "Job")
	if !allowed {
		return false
	}
	parentID := parentKind + "/" + String(meta["namespace"]) + "/" + String(ref["name"])
	parent := live[parentID]
	if parent == nil || String(ref["uid"]) == "" || String(ref["uid"]) != String(Map(parent["metadata"])["uid"]) || String(ref["apiVersion"]) != String(parent["apiVersion"]) {
		return false
	}
	return verifiedControllerChild(parentID, live, declared, stack, visiting)
}

func (p *Plan) CheckWorkloadInventory(objects []Object) error {
	if p == nil {
		return errors.New("complete compiled plan required")
	}
	if p.Operation == "scoped" {
		return nil
	}
	live := map[string]Object{}
	declared := map[string]bool{}
	for _, resource := range p.Resources {
		declared[resource.ID] = true
	}
	for _, object := range objects {
		if live[ID(object)] != nil {
			return errors.New("duplicate runtime owner")
		}
		live[ID(object)] = object
	}
	ids := make([]string, 0, len(live))
	for id := range live {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		object := live[id]
		if controller(String(object["kind"])) && !declared[id] && strings.HasPrefix(String(Map(object["metadata"])["namespace"]), p.Stack+"-") && !HistoricalTerminalWorkload(id, live, p.Stack) && !verifiedControllerChild(id, live, declared, p.Stack, map[string]bool{}) {
			return blocked("deployment.inventory", id, "a new unknown workload appeared during qualification or execution", "stop and declare its maintained component or controlled retirement; repeat complete preflight")
		}
	}
	return nil
}

const deploymentRevisionAnnotation = "deployment.kubernetes.io/revision"

// Deployment owns this rollout counter. It is not user intent, and changes
// after the original update response. Keep every other annotation exact.
func normalizeDeploymentRevision(object Object) {
	if object["apiVersion"] != "apps/v1" || object["kind"] != "Deployment" {
		return
	}
	metadata := Map(object["metadata"])
	annotations, ok := metadata["annotations"].(map[string]any)
	if !ok {
		return
	}
	delete(annotations, deploymentRevisionAnnotation)
	if len(annotations) == 0 {
		delete(metadata, "annotations")
	}
}

// Projection removes only server metadata and status; configuration drift remains.
func Projection(o Object) Object {
	c := Clone(o)
	defaultEmptyPodEnvironment(c)
	delete(c, "status")
	m := Map(c["metadata"])
	for _, k := range []string{"resourceVersion", "generation", "creationTimestamp", "managedFields"} {
		delete(m, k)
	}
	return c
}

// Matches permits Kubernetes defaults and unrelated metadata, not desired drift.
func Matches(desired, current Object) bool {
	desired, current = Clone(desired), Clone(current)
	normalizeDeploymentRevision(desired)
	normalizeDeploymentRevision(current)
	defaultEmptyPodEnvironment(desired)
	defaultEmptyPodEnvironment(current)
	for key, value := range desired {
		if key == "metadata" {
			for _, field := range []string{"name", "namespace", "labels", "annotations"} {
				v, ok := Map(value)[field]
				if !ok {
					continue
				}
				if field == "labels" || field == "annotations" {
					// Body merges only declared keys, preserving unrelated metadata.
					// A null or empty outer map declares no keys to compare.
					m, isMap := v.(map[string]any)
					if v == nil || (isMap && len(m) == 0) {
						continue
					}
				}
				if !subset(v, Map(current[key])[field]) {
					return false
				}
			}
			continue
		}
		if !subset(value, current[key]) {
			return false
		}
	}
	return true
}

// EnvVar.value defaults null or absent input to the empty string, which native
// JSON omits.
// A valueFrom source is never equivalent: leave it untouched so explicit empty
// desired values still reject a webhook injecting a Secret/ConfigMap source.
func defaultEmptyPodEnvironment(object Object) {
	var pod Object
	switch object["kind"] {
	case "Pod":
		pod = Map(object["spec"])
	case "Deployment", "StatefulSet", "DaemonSet", "Job":
		pod = Map(Map(Map(object["spec"])["template"])["spec"])
	case "CronJob":
		pod = Map(Map(Map(Map(Map(object["spec"])["jobTemplate"])["spec"])["template"])["spec"])
	default:
		return
	}
	for _, field := range []string{"containers", "initContainers"} {
		containers, _ := pod[field].([]any)
		for _, container := range containers {
			mounts, _ := Map(container)["volumeMounts"].([]any)
			for _, entry := range mounts {
				mount := Map(entry)
				if mount != nil {
					if _, declared := mount["readOnly"]; !declared {
						mount["readOnly"] = false
					}
				}
			}
			env, _ := Map(container)["env"].([]any)
			for _, entry := range env {
				value := Map(entry)
				if value == nil {
					continue
				}
				if value["value"] == nil && value["valueFrom"] == nil {
					value["value"] = ""
				}
			}
		}
	}
}
func subset(want, have any) bool {
	if m, ok := want.(map[string]any); ok {
		actual, ok := have.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range m {
			if !subset(v, actual[k]) {
				return false
			}
		}
		return true
	}
	if list, ok := want.([]any); ok {
		actual, ok := have.([]any)
		if !ok || len(list) != len(actual) {
			return false
		}
		for i, value := range list {
			if !subset(value, actual[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(want, have)
}

func retainAllocatedFields(desired, current Object) error {
	fields := []string{}
	switch desired["kind"] {
	case "Service":
		fields = []string{"clusterIP", "clusterIPs", "ipFamilies", "ipFamilyPolicy", "healthCheckNodePort"}
	case "PersistentVolumeClaim":
		fields = []string{"volumeName", "storageClassName", "volumeMode"}
	default:
		return nil
	}
	want, old := Map(desired["spec"]), Map(current["spec"])
	for _, field := range fields {
		if value, exists := old[field]; exists {
			if v, declared := want[field]; declared && !reflect.DeepEqual(v, value) {
				return blocked("deployment.immutable", ID(desired), "allocated "+field+" change requires a migration", "preserve the allocation or complete its reviewed resource migration")
			}
			want[field] = value
		}
	}
	return nil
}

func validateLifecycle(id string, desired, current Object) error {
	want, old := Map(desired["spec"]), Map(current["spec"])
	fields := []string{}
	switch desired["kind"] {
	case "Deployment", "DaemonSet":
		fields = []string{"selector"}
	case "StatefulSet":
		fields = []string{"selector", "serviceName", "volumeClaimTemplates", "podManagementPolicy"}
	case "PersistentVolumeClaim":
		fields = []string{"storageClassName", "accessModes", "volumeMode", "volumeName"}
	case "Secret", "ConfigMap":
		if current["immutable"] == true && !reflect.DeepEqual(desired["data"], current["data"]) {
			return blocked("deployment.immutable", id, "immutable lifecycle content differs", "use the managed Secret or configuration lifecycle")
		}
	case "Job", "Pod":
		if !Matches(desired, current) {
			return blocked("deployment.migration", id, "existing Job differs from the selected migration", "use a release-specific migration Job and retain failed evidence")
		}
	}
	for _, field := range fields {
		if value, ok := want[field]; ok && !subset(value, old[field]) {
			return blocked("deployment.immutable", id, "immutable field "+field+" differs", "complete a reviewed migration; delete/recreate is not an upgrade")
		}
	}
	return nil
}

func (p *Plan) SHA256() string { return Digest(p) }

// Body is shared by admission and execution; current API identity fences writes.
func (r Resource) Body(current Object) (Object, error) {
	if r.Action == "create" {
		if current != nil {
			return nil, blocked("deployment.drift", r.ID, "planned absent owner now exists", "repeat planning and qualification")
		}
		return Clone(r.Desired), nil
	}
	baseline, observed := Clone(r.Baseline), Projection(current)
	normalizeDeploymentRevision(baseline)
	normalizeDeploymentRevision(observed)
	if current == nil || String(Map(current["metadata"])["uid"]) != r.UID || !reflect.DeepEqual(observed, baseline) {
		return nil, blocked("deployment.drift", r.ID, "owner or configuration changed after qualification", "repeat planning and qualification; do not overwrite concurrent updates")
	}
	if r.Action == "retain" {
		return nil, nil
	}
	if r.Action == "retire" {
		body := Clone(r.Desired)
		metadata := Map(body["metadata"])
		metadata["uid"], metadata["resourceVersion"] = r.UID, Map(current["metadata"])["resourceVersion"]
		return body, nil
	}
	if r.Action != "update" {
		return nil, errors.New("unsupported resource action")
	}
	body := Clone(current)
	delete(body, "status")
	for k, v := range r.Desired {
		if k != "metadata" {
			body[k] = v
		}
	}
	m := Map(body["metadata"])
	for _, k := range []string{"labels", "annotations"} {
		for field, value := range Map(Map(r.Desired["metadata"])[k]) {
			if k == "annotations" && field == deploymentRevisionAnnotation && r.Desired["apiVersion"] == "apps/v1" && r.Desired["kind"] == "Deployment" {
				continue // preserve the current controller counter in an old frozen plan
			}
			target := Map(m[k])
			if target == nil {
				target = Object{}
				m[k] = target
			}
			target[field] = value
		}
	}
	if String(m["resourceVersion"]) == "" {
		return nil, blocked("deployment.drift", r.ID, "API resource version is absent", "restore verified Kubernetes access")
	}
	return body, nil
}

func retirable(kind string) bool {
	return controller(kind) || kind == "Job" || kind == "CronJob" || kind == "Service" || kind == "NetworkPolicy" || kind == "Ingress" || kind == "IngressClass" || kind == "ValidatingWebhookConfiguration" || kind == "Role" || kind == "RoleBinding" || kind == "ClusterRole" || kind == "ClusterRoleBinding" || kind == "ServiceAccount" || kind == "PodDisruptionBudget" || kind == "HorizontalPodAutoscaler"
}

type Summary struct {
	SchemaVersion int               `json:"schema_version"`
	Environment   string            `json:"environment"`
	Operation     string            `json:"operation"`
	PlanSHA256    string            `json:"plan_sha256"`
	Resources     []SummaryResource `json:"resources"`
}
type SummaryResource struct {
	ID     string `json:"id"`
	Phase  string `json:"phase"`
	Action string `json:"action"`
}

func (p *Plan) Summary() Summary {
	s := Summary{Version, p.Environment, p.Operation, p.SHA256(), nil}
	for _, r := range p.Resources {
		s.Resources = append(s.Resources, SummaryResource{r.ID, r.Phase, r.Action})
	}
	return s
}

// Only an existing UID-bound admission webhook with exact selected Service
// endpoints may keep its original non-prefixed Helm name.
func originalAdmissionOwner(desired, current Object, stack string, adoptions []map[string]string) bool {
	if desired["kind"] != "ValidatingWebhookConfiguration" || current == nil || len(adoptions) != 1 {
		return false
	}
	uid := String(Map(current["metadata"])["uid"])
	if uid == "" || adoptions[0][ID(desired)] != uid {
		return false
	}
	for _, object := range []Object{desired, current} {
		hooks, ok := object["webhooks"].([]any)
		if !ok || len(hooks) == 0 {
			return false
		}
		for _, value := range hooks {
			client := Map(Map(value)["clientConfig"])
			service := Map(client["service"])
			if client["url"] != nil || service["namespace"] != stack+"-ingress" || service["name"] != "ingress-nginx-controller-admission" {
				return false
			}
		}
	}
	return true
}
