package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const lockName = "rtk-core-recovery"

// Exec never includes subprocess output or arguments in errors: database dumps,
// credentials and operator checks can contain secrets even in stderr.
type Executor func(context.Context, []string, io.Reader, io.Writer) error

func QuietExec(ctx context.Context, argv []string, in io.Reader, out io.Writer) error {
	if len(argv) == 0 {
		return errors.New("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return errors.New("subprocess failed (output suppressed)")
	}
	return nil
}

type Engine struct {
	Config     Config
	Kubeconfig string
	SecretRoot string
	Kubectl    string
	Exec       Executor
	Publish    func(context.Context, Config, string, string) error
}

func (e *Engine) publish(ctx context.Context, id, file string) error {
	if e.Publish != nil {
		return e.Publish(ctx, e.Config, id, file)
	}
	return Upload(ctx, e.Config, id, file)
}

type SavedWorkload struct {
	Workload
	Replicas int `json:"replicas"`
}
type Journal struct {
	Version             int             `json:"version"`
	ID                  string          `json:"operation_id"`
	Environment         string          `json:"environment"`
	Stack               string          `json:"stack"`
	Operation           string          `json:"operation"`
	Phase               string          `json:"phase"`
	ConfigurationSHA256 string          `json:"configuration_sha256"`
	NamespaceUID        string          `json:"namespace_uid"`
	Workloads           []SavedWorkload `json:"workloads"`
	BackupID            string          `json:"backup_id,omitempty"`
	SafetyBackupID      string          `json:"safety_backup_id,omitempty"`
	VerifiedAt          *time.Time      `json:"verified_at,omitempty"`
}

func (e *Engine) command(ctx context.Context, argv []string, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(e.Config.TimeoutSeconds)*time.Second)
	defer cancel()
	run := e.Exec
	if run == nil {
		run = QuietExec
	}
	return run(ctx, argv, in, out)
}
func (e *Engine) kube(ctx context.Context, in io.Reader, out io.Writer, args ...string) error {
	executable := e.Kubectl
	if executable == "" {
		executable = "kubectl"
	}
	return e.command(ctx, append([]string{executable, "--kubeconfig", e.Kubeconfig, "--request-timeout=" + strconv.Itoa(e.Config.TimeoutSeconds) + "s"}, args...), in, out)
}
func (e *Engine) get(ctx context.Context, target any, args ...string) error {
	var out limitedBuffer
	out.limit = 16 << 20
	if err := e.kube(ctx, nil, &out, append(args, "-o", "json")...); err != nil {
		return err
	}
	if err := json.Unmarshal(out.Bytes(), target); err != nil {
		return errors.New("invalid Kubernetes response")
	}
	return nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit int64
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if int64(b.Len()+len(p)) > b.limit {
		return 0, errors.New("command output limit exceeded")
	}
	return b.Buffer.Write(p)
}

func (e *Engine) checks(ctx context.Context, checks []Check) error {
	for _, check := range checks {
		if err := e.command(ctx, check.Argv, nil, io.Discard); err != nil {
			return fmt.Errorf("check %s failed; maintenance remains active", check.ID)
		}
	}
	return nil
}

type kubeObject struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name            string `json:"name"`
		Namespace       string `json:"namespace"`
		UID             string `json:"uid"`
		ResourceVersion string `json:"resourceVersion"`
		OwnerReferences []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
				InitContainers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"initContainers"`
			} `json:"spec"`
		} `json:"template"`
		Replicas *int  `json:"replicas"`
		Suspend  *bool `json:"suspend"`
		Volumes  []struct {
			PersistentVolumeClaim *struct {
				ClaimName string `json:"claimName"`
			} `json:"persistentVolumeClaim"`
		} `json:"volumes"`
	} `json:"spec"`
	Status struct {
		CompletionTime string `json:"completionTime"`
		Phase          string `json:"phase"`
		Active         int    `json:"active"`
		Replicas       int    `json:"replicas"`
	} `json:"status"`
}
type kubeList struct {
	Items []kubeObject `json:"items"`
}

func workloadKey(w Workload) string { return w.Namespace + "/" + w.Kind + "/" + w.Name }

// Preflight refuses unknown workloads/PVCs, unsuspended CronJobs, any HPA,
// DaemonSet or active Job. Removing controllers is an operator-reviewed step,
// not an automatic side effect hidden in a backup command.
func (e *Engine) Preflight(ctx context.Context) ([]SavedWorkload, error) {
	if err := e.Config.Validate(); err != nil {
		return nil, err
	}
	if err := e.ValidateLayout(); err != nil {
		return nil, err
	}
	if e.Publish == nil {
		if _, err := RemoteClient(e.Config.Remote); err != nil {
			return nil, err
		}
	}
	known := map[string]Workload{}
	for _, w := range e.Config.Workloads {
		known[workloadKey(w)] = w
	}
	pvc := map[string]bool{}
	for _, c := range e.Config.Components {
		if c.PVC != "" {
			pvc[c.Namespace+"/"+c.PVC] = true
		}
	}
	for _, v := range e.Config.ExcludedPVCs {
		if v.Reason == "" {
			return nil, errors.New("PVC exclusion requires a reason")
		}
		pvc[v.Namespace+"/"+v.Name] = true
	}
	seen := map[string]bool{}
	var saved []SavedWorkload
	for _, ns := range e.Config.Namespaces {
		var list kubeList
		if err := e.get(ctx, &list, "-n", ns, "get", "deployments,statefulsets,daemonsets,hpa,cronjobs,jobs,pvc"); err != nil {
			return nil, err
		}
		for _, o := range list.Items {
			switch o.Kind {
			case "Deployment", "StatefulSet":
				key := ns + "/" + strings.ToLower(o.Kind) + "/" + o.Metadata.Name
				w, ok := known[key]
				if !ok {
					return nil, errors.New("unclassified workload; update inventory before maintenance")
				}
				seen[key] = true
				replicas := 1
				if o.Spec.Replicas != nil {
					replicas = *o.Spec.Replicas
				}
				saved = append(saved, SavedWorkload{w, replicas})
			case "CronJob":
				if o.Spec.Suspend == nil || !*o.Spec.Suspend {
					return nil, errors.New("suspend CronJobs before maintenance")
				}
			case "Job":
				if o.Status.Active > 0 || (o.Status.CompletionTime == "" && (o.Spec.Suspend == nil || !*o.Spec.Suspend)) {
					return nil, errors.New("active/pending/failed Job must be suspended or removed before maintenance")
				}
			case "PersistentVolumeClaim":
				if !pvc[ns+"/"+o.Metadata.Name] {
					return nil, errors.New("unclassified PVC; add component or explicit exclusion")
				}
			default:
				return nil, errors.New("HPA/DaemonSet or unsupported controller blocks maintenance")
			}
		}
	}
	if len(seen) != len(known) {
		return nil, errors.New("configured workload missing from target")
	}
	for _, ns := range e.Config.Namespaces {
		var rs, pods kubeList
		if err := e.get(ctx, &rs, "-n", ns, "get", "replicasets"); err != nil {
			return nil, err
		}
		if err := e.get(ctx, &pods, "-n", ns, "get", "pods"); err != nil {
			return nil, err
		}
		owners := map[string]Workload{}
		for _, w := range e.Config.Workloads {
			if w.Namespace == ns {
				owners[w.Kind+"/"+w.Name] = w
			}
		}
		for _, r := range rs.Items {
			for _, o := range r.Metadata.OwnerReferences {
				if w, ok := owners[strings.ToLower(o.Kind)+"/"+o.Name]; ok {
					owners["replicaset/"+r.Metadata.Name] = w
				}
			}
		}
		for _, p := range pods.Items {
			if p.Status.Phase == "Succeeded" || p.Status.Phase == "Failed" {
				continue
			}
			var owner Workload
			found := false
			for _, o := range p.Metadata.OwnerReferences {
				if w, ok := owners[strings.ToLower(o.Kind)+"/"+o.Name]; ok {
					owner = w
					found = true
					break
				}
			}
			if !found {
				return nil, errors.New("unmanaged or unsupported Pod writer blocks maintenance")
			}
			covered := false
			for _, c := range e.Config.Components {
				if (c.Kind == "postgres" || c.Kind == "redis") && c.Namespace == ns && c.Pod == p.Metadata.Name {
					covered = true
					if owner.Role != "data" {
						return nil, errors.New("logical dump database Pod must have workload role data")
					}
				}
			}
			if owner.Role == "data" && !covered {
				return nil, errors.New("online data Pod has no logical backup component")
			}
		}
	}
	if err := e.checkDatabases(ctx); err != nil {
		return nil, err
	}
	if err := e.checks(ctx, e.Config.PreflightChecks); err != nil {
		return nil, err
	}
	return saved, nil
}

// Never capture an archive inside the SecretStore tree being walked: doing so
// could recursively read the output while it grows. Git trees are also refused
// because temporary plaintext capture must not enter a repository.
func (e *Engine) ValidateLayout() error {
	within := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && (rel == "." || SafeRelative(filepath.ToSlash(rel)))
	}
	if within(e.SecretRoot, e.Config.Directory) || within(e.Config.Directory, e.SecretRoot) {
		return errors.New("backup directory and SecretStore must be disjoint")
	}
	for p := e.Config.Directory; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
			return errors.New("backup directory must be outside Git repositories")
		} else if !os.IsNotExist(err) {
			return err
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}

func (e *Engine) checkDatabases(ctx context.Context) error {
	for _, c := range e.Config.Components {
		if c.Kind != "postgres" || c.Database != "@globals" {
			continue
		}
		b, err := e.podOutput(ctx, c, "psql", "-X", "-U", c.User, "-d", "postgres", "-At", "-v", "ON_ERROR_STOP=1", "-c", "SELECT datname FROM pg_database WHERE NOT datistemplate AND datname <> 'postgres' ORDER BY datname")
		if err != nil {
			return err
		}
		expected := map[string]bool{}
		for _, d := range e.Config.Components {
			if d.Kind == "postgres" && d.Namespace == c.Namespace && d.Pod == c.Pod && d.Container == c.Container && d.Database != "@globals" {
				expected[d.Database] = true
			}
		}
		for _, name := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if !expected[name] {
				return errors.New("unclassified PostgreSQL database")
			}
			delete(expected, name)
		}
		if len(expected) != 0 {
			return errors.New("configured PostgreSQL database missing")
		}
		b, err = e.podOutput(ctx, c, "psql", "-X", "-U", c.User, "-d", "postgres", "-At", "-v", "ON_ERROR_STOP=1", "-c", "SELECT count(*) FROM pg_tablespace WHERE spcname NOT IN ('pg_default','pg_global')")
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(b)) != "0" {
			return errors.New("custom PostgreSQL tablespaces require a reviewed adapter")
		}
	}
	return nil
}

func (e *Engine) journalPath() string { return filepath.Join(e.SecretRoot, "recovery-state.json") }
func (e *Engine) save(ctx context.Context, j *Journal, create bool) error {
	b, _ := json.Marshal(j)
	obj := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": lockName, "namespace": e.Config.LockNamespace}, "data": map[string]string{"journal": string(b)}}
	payload, _ := json.Marshal(obj)
	action := "apply"
	if create {
		action = "create"
	}
	if err := e.kube(ctx, bytes.NewReader(payload), io.Discard, "-n", e.Config.LockNamespace, action, "-f", "-"); err != nil {
		return errors.New("cannot persist recovery lock; do not start a concurrent operation")
	}
	return WriteJSON(e.journalPath(), j)
}
func (e *Engine) Load(ctx context.Context) (Journal, error) {
	var j Journal
	var obj struct {
		Data map[string]string `json:"data"`
	}
	if err := e.get(ctx, &obj, "-n", e.Config.LockNamespace, "get", "configmap", lockName); err != nil {
		return j, errors.New("recovery lock unavailable; check exact target cluster/namespace")
	}
	if err := Decode(strings.NewReader(obj.Data["journal"]), &j); err != nil {
		return j, err
	}
	var ns kubeObject
	if err := e.get(ctx, &ns, "get", "namespace", e.Config.LockNamespace); err != nil {
		return j, err
	}
	if j.Version != Version || j.Environment != e.Config.Environment || j.Stack != e.Config.Stack || j.ConfigurationSHA256 != Digest(e.Config) || j.NamespaceUID != ns.Metadata.UID {
		return j, errors.New("recovery journal target/configuration mismatch")
	}
	if !Name.MatchString(j.ID) || (j.Operation != "backup" && j.Operation != "restore") {
		return j, errors.New("invalid journal operation")
	}
	known := map[string]Workload{}
	for _, w := range e.Config.Workloads {
		known[workloadKey(w)] = w
	}
	for _, w := range j.Workloads {
		key := workloadKey(w.Workload)
		if original, ok := known[key]; !ok || original != w.Workload || w.Replicas < 0 {
			return j, errors.New("journal workload inventory differs from config")
		}
		delete(known, key)
	}
	if len(known) != 0 {
		return j, errors.New("journal missing workload")
	}
	return j, nil
}

func (e *Engine) scale(ctx context.Context, w SavedWorkload, replicas int) error {
	return e.kube(ctx, nil, io.Discard, "-n", w.Namespace, "scale", w.Kind+"/"+w.Name, "--replicas="+strconv.Itoa(replicas))
}
func (e *Engine) waitStopped(ctx context.Context, w SavedWorkload) error {
	// Selector comes from the live controller (supports matchExpressions via
	// kubectl's JSONPath selector is intentionally not guessed). Check owned pods
	// by querying ReplicaSets as well as direct StatefulSet ownership.
	return e.poll(ctx, func() (bool, error) {
		owners := map[string]bool{w.Kind + "/" + w.Name: true}
		if w.Kind == "deployment" {
			var rs kubeList
			if err := e.get(ctx, &rs, "-n", w.Namespace, "get", "replicasets"); err != nil {
				return false, err
			}
			for _, r := range rs.Items {
				for _, o := range r.Metadata.OwnerReferences {
					if o.Kind == "Deployment" && o.Name == w.Name {
						owners["replicaset/"+r.Metadata.Name] = true
					}
				}
			}
		}
		var pods kubeList
		if err := e.get(ctx, &pods, "-n", w.Namespace, "get", "pods"); err != nil {
			return false, err
		}
		for _, p := range pods.Items {
			for _, o := range p.Metadata.OwnerReferences {
				if owners[strings.ToLower(o.Kind)+"/"+o.Name] {
					return false, nil
				}
			}
		}
		return true, nil
	})
}
func (e *Engine) poll(ctx context.Context, check func() (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(e.Config.TimeoutSeconds)*time.Second)
	defer cancel()
	for {
		ok, err := check()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("maintenance wait timed out; lock retained")
		case <-time.After(time.Second):
		}
	}
}

func (e *Engine) Begin(ctx context.Context, operation, id string) (Journal, error) {
	j := Journal{}
	if err := PrivateDirectory(e.SecretRoot); err != nil {
		return j, err
	}
	if _, err := os.Lstat(e.journalPath()); err == nil {
		return j, errors.New("local recovery journal exists; use status/verify/resume")
	} else if !os.IsNotExist(err) {
		return j, err
	}
	saved, err := e.Preflight(ctx)
	if err != nil {
		return j, err
	}
	var ns kubeObject
	if err = e.get(ctx, &ns, "get", "namespace", e.Config.LockNamespace); err != nil {
		return j, err
	}
	j = Journal{Version: Version, ID: id, Environment: e.Config.Environment, Stack: e.Config.Stack, Operation: operation, Phase: "quiescing", ConfigurationSHA256: Digest(e.Config), NamespaceUID: ns.Metadata.UID, Workloads: saved}
	// Journal exists before the first scale. A competing controller cannot create
	// the same ConfigMap; failures after this point deliberately retain the lock.
	if err = e.save(ctx, &j, true); err != nil {
		return j, err
	}
	for _, w := range saved {
		if w.Role == "application" {
			if err = e.scale(ctx, w, 0); err != nil {
				return j, err
			}
		}
	}
	for _, w := range saved {
		if w.Role == "application" {
			if err = e.waitStopped(ctx, w); err != nil {
				return j, err
			}
		}
	}
	if err = e.checks(ctx, e.Config.QuiescenceChecks); err != nil {
		return j, err
	}
	for _, w := range saved {
		if w.Role == "offline" {
			if err = e.scale(ctx, w, 0); err != nil {
				return j, err
			}
			if err = e.waitStopped(ctx, w); err != nil {
				return j, err
			}
		}
	}
	j.Phase = "paused"
	return j, e.save(ctx, &j, false)
}

func (e *Engine) AssertPaused(ctx context.Context, j Journal) error {
	for _, w := range j.Workloads {
		if w.Role != "data" {
			var live kubeObject
			if err := e.get(ctx, &live, "-n", w.Namespace, "get", w.Kind, w.Name); err != nil {
				return err
			}
			if live.Spec.Replicas == nil || *live.Spec.Replicas != 0 {
				return errors.New("workload resumed outside recovery; stop and investigate")
			}
			if err := e.waitStopped(ctx, w); err != nil {
				return err
			}
		}
	}
	return e.checks(ctx, e.Config.QuiescenceChecks)
}

// A per-invocation local mutex prevents two resume/apply processes racing.
// Persistent maintenance state remains in both SecretStore and ConfigMap.
func (e *Engine) InvocationLock(ctx context.Context) (func(), error) {
	if err := PrivateDirectory(e.SecretRoot); err != nil {
		return nil, err
	}
	p := filepath.Join(e.SecretRoot, "recovery-command.lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, errors.New("recovery command lock exists; do not remove until its process is confirmed stopped")
	}
	fmt.Fprintln(f, os.Getpid())
	f.Close()
	object := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]string{"name": lockName + "-command", "namespace": e.Config.LockNamespace}}
	b, _ := json.Marshal(object)
	if err = e.kube(ctx, bytes.NewReader(b), io.Discard, "-n", e.Config.LockNamespace, "create", "-f", "-"); err != nil {
		os.Remove(p)
		return nil, errors.New("cluster recovery command lock exists/unavailable; concurrent operations refused")
	}
	return func() {
		if err := e.kube(context.WithoutCancel(ctx), nil, io.Discard, "-n", e.Config.LockNamespace, "delete", "configmap", lockName+"-command", "--wait=true"); err != nil {
			fmt.Fprintln(os.Stderr, "Recovery command lock cleanup failed; confirm process stopped before manually removing the exact command locks.")
			return
		}
		os.Remove(p)
	}, nil
}
