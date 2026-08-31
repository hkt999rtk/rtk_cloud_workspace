package recovery

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

func (e *Engine) pod(ctx context.Context, c Component, in io.Reader, out io.Writer, args ...string) error {
	argv := []string{"-n", c.Namespace, "exec", "-i", c.Pod}
	if c.Container != "" {
		argv = append(argv, "-c", c.Container)
	}
	argv = append(argv, "--")
	return e.kube(ctx, in, out, append(argv, args...)...)
}
func (e *Engine) podOutput(ctx context.Context, c Component, args ...string) ([]byte, error) {
	var b limitedBuffer
	b.limit = e.Config.MaxArchiveBytes
	err := e.pod(ctx, c, nil, &b, args...)
	return b.Bytes(), err
}

func (e *Engine) helper(ctx context.Context, c Component, fn func(Component) error) error {
	var pods kubeList
	if err := e.get(ctx, &pods, "-n", c.Namespace, "get", "pods"); err != nil {
		return err
	}
	for _, p := range pods.Items {
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == c.PVC {
				return errors.New("PVC still mounted by a pod; offline capture/restore refused")
			}
		}
	}
	name := "rtk-recovery-" + c.ID
	if len(name) > 63 {
		return errors.New("component id too long for helper name")
	}
	obj := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": name, "namespace": c.Namespace, "labels": map[string]string{"app.kubernetes.io/managed-by": "rtk-core-recovery"}}, "spec": map[string]any{
		"restartPolicy": "Never", "automountServiceAccountToken": false,
		"containers": []any{map[string]any{"name": "files", "image": c.Image, "command": []string{"sh", "-c", "sleep 86400"}, "resources": map[string]any{"requests": map[string]string{"cpu": "50m", "memory": "64Mi"}, "limits": map[string]string{"cpu": "500m", "memory": "256Mi"}}, "securityContext": map[string]any{"runAsUser": 0, "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []string{"ALL"}, "add": []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"}}}, "volumeMounts": []any{map[string]string{"name": "data", "mountPath": "/backup"}}}},
		"volumes":    []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]string{"claimName": c.PVC}}}}}
	b, _ := json.Marshal(obj)
	if err := e.kube(ctx, bytes.NewReader(b), io.Discard, "-n", c.Namespace, "create", "-f", "-"); err != nil {
		return err
	}
	// No defer hiding a deletion failure: a leftover helper is a hard blocker.
	target := c
	target.Pod = name
	target.Container = "files"
	err := e.kube(ctx, nil, io.Discard, "-n", c.Namespace, "wait", "--for=condition=Ready", "pod/"+name, "--timeout="+strconv.Itoa(e.Config.TimeoutSeconds)+"s")
	if err == nil {
		err = fn(target)
	}
	cleanup := e.kube(context.WithoutCancel(ctx), nil, io.Discard, "-n", c.Namespace, "delete", "pod", name, "--wait=true", "--timeout="+strconv.Itoa(e.Config.TimeoutSeconds)+"s")
	if cleanup != nil {
		return errors.New("helper cleanup failed; remove only the recorded recovery helper before proceeding")
	}
	return err
}

func (e *Engine) Capture(ctx context.Context, dir string) error {
	if len(e.Config.Workloads) > 0 {
		inventory, err := e.releaseInventory(ctx)
		if err != nil {
			return err
		}
		if err = WriteJSON(filepath.Join(dir, "release-inventory.json"), inventory); err != nil {
			return err
		}
	}
	for _, c := range e.Config.Components {
		dest := filepath.Join(dir, c.ID+".data")
		if c.Kind == "secretstore" {
			if err := PackPaths(e.SecretRoot, c.Paths, dest); err != nil {
				return fmt.Errorf("capture %s failed: %w", c.ID, err)
			}
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		bounded := &boundedWriter{writer: out, remaining: e.Config.MaxArchiveBytes}
		switch c.Kind {
		case "postgres":
			if c.Database == "@globals" {
				err = e.pod(ctx, c, nil, bounded, "pg_dumpall", "-U", c.User, "--globals-only", "--no-tablespaces")
			} else {
				err = e.pod(ctx, c, nil, bounded, "pg_dump", "-U", c.User, "--format=custom", "--create", "--dbname", c.Database)
			}
		case "redis":
			err = e.captureRedis(ctx, c, bounded)
		case "volume":
			err = e.helper(ctx, c, func(helper Component) error {
				return e.pod(ctx, helper, nil, bounded, "tar", "-cpf", "-", "-C", "/backup", ".")
			})
		case "k8s-object":
			var object map[string]any
			err = e.get(ctx, &object, "-n", c.Namespace, "get", c.Resource, c.ResourceName)
			if err == nil {
				sanitizeObject(object, c)
				err = json.NewEncoder(bounded).Encode(object)
			}
		default:
			err = errors.New("unsupported capture component")
		}
		closeErr := out.Close()
		if err != nil {
			return fmt.Errorf("capture %s failed: %w", c.ID, err)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return e.ValidateArtifacts(ctx, dir)
}

type boundedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("artifact exceeds limit")
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func sanitizeObject(o map[string]any, c Component) {
	for k := range o {
		switch k {
		case "apiVersion", "kind", "data", "binaryData", "type", "immutable":
		default:
			delete(o, k)
		}
	}
	o["metadata"] = map[string]string{"name": c.ResourceName, "namespace": c.Namespace}
}

type redisEntry struct {
	Key  string `json:"key"`
	Dump []byte `json:"dump"`
}

func validRedisKey(key string, c Component) bool {
	if key == "" || strings.ContainsAny(key, "\r\n\x00") {
		return false
	}
	for _, p := range c.Prefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

func excludedRedisKey(key string, c Component) bool {
	for _, excluded := range c.ExcludeKeys {
		if key == excluded {
			return true
		}
	}
	return false
}
func (e *Engine) redisKeys(ctx context.Context, c Component) ([]string, error) {
	seen := map[string]bool{}
	for _, prefix := range c.Prefixes {
		cursor := "0"
		for {
			b, err := e.podOutput(ctx, c, "redis-cli", "--json", "SCAN", cursor, "MATCH", prefix+"*", "COUNT", "1000")
			if err != nil {
				return nil, err
			}
			var reply []json.RawMessage
			if json.Unmarshal(b, &reply) != nil || len(reply) != 2 {
				return nil, errors.New("invalid Redis SCAN response")
			}
			var keys []string
			if json.Unmarshal(reply[0], &cursor) != nil || json.Unmarshal(reply[1], &keys) != nil {
				return nil, errors.New("invalid Redis SCAN data")
			}
			if _, err = strconv.ParseUint(cursor, 10, 64); err != nil {
				return nil, errors.New("invalid Redis cursor")
			}
			for _, key := range keys {
				if !validRedisKey(key, c) {
					return nil, errors.New("unsafe Redis key")
				}
				if excludedRedisKey(key, c) {
					continue
				}
				seen[key] = true
				if len(seen) > 1000000 {
					return nil, errors.New("Redis key limit exceeded")
				}
			}
			if cursor == "0" {
				break
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}
func (e *Engine) captureRedis(ctx context.Context, c Component, out io.Writer) error {
	keys, err := e.redisKeys(ctx, c)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	for _, key := range keys {
		ttl, err := e.podOutput(ctx, c, "redis-cli", "--raw", "PTTL", key)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(ttl)) != "-1" {
			return errors.New("durable Redis inventory contains expiring/missing keys; separate leases/cache from durable prefixes")
		}
		// -D suppresses the delimiter; binary serialized values are never decoded.
		dump, err := e.podOutput(ctx, c, "redis-cli", "-e", "-D", "", "--raw", "DUMP", key)
		if err != nil {
			return err
		}
		if len(dump) < 10 {
			return errors.New("invalid Redis DUMP")
		}
		if err = encoder.Encode(redisEntry{key, dump}); err != nil {
			return err
		}
	}
	return nil
}
func readRedis(path string, c Component, limit int64, visit func(redisEntry) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, limit+1))
	d.DisallowUnknownFields()
	seen := map[string]bool{}
	for {
		var v redisEntry
		err = d.Decode(&v)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errors.New("invalid Redis archive")
		}
		if !validRedisKey(v.Key, c) || excludedRedisKey(v.Key, c) || len(v.Dump) < 10 || seen[v.Key] || len(seen) >= 1000000 {
			return errors.New("invalid/duplicate Redis archive key")
		}
		seen[v.Key] = true
		if visit != nil {
			if err = visit(v); err != nil {
				return err
			}
		}
	}
}

func (e *Engine) restoreRedis(ctx context.Context, c Component, path string) error {
	keys, err := e.redisKeys(ctx, c)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if _, err = e.podOutput(ctx, c, "redis-cli", "-e", "DEL", key); err != nil {
			return err
		}
	}
	return readRedis(path, c, e.Config.MaxArchiveBytes, func(v redisEntry) error {
		var b limitedBuffer
		b.limit = 1024
		if err := e.pod(ctx, c, bytes.NewReader(v.Dump), &b, "redis-cli", "-e", "--raw", "-x", "RESTORE", v.Key, "0"); err != nil {
			return err
		}
		if strings.TrimSpace(b.String()) != "OK" {
			return errors.New("Redis RESTORE did not return OK")
		}
		return nil
	})
}

func (e *Engine) ValidateArtifacts(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	expected := map[string]bool{"manifest.json": true}
	if len(e.Config.Workloads) > 0 {
		expected["release-inventory.json"] = true
		f, err := os.Open(filepath.Join(dir, "release-inventory.json"))
		if err != nil {
			return errors.New("backup lacks release inventory")
		}
		var saved map[string][]string
		err = Decode(io.LimitReader(f, 4<<20), &saved)
		f.Close()
		if err != nil {
			return err
		}
		live, err := e.releaseInventory(ctx)
		if err != nil {
			return err
		}
		if Digest(saved) != Digest(live) {
			return errors.New("target workload images differ from backup; deploy the matching release under isolation first")
		}
	}
	for _, c := range e.Config.Components {
		name := c.ID + ".data"
		expected[name] = true
		path := filepath.Join(dir, name)
		i, err := os.Lstat(path)
		if err != nil || !i.Mode().IsRegular() || i.Size() > e.Config.MaxArchiveBytes {
			return errors.New("missing/nonregular/oversized component artifact")
		}
		switch c.Kind {
		case "secretstore":
			err = WalkArchive(path, e.Config.MaxArchiveBytes, func(h *tar.Header, _ io.Reader) error {
				if !selectedSecret(c, h.Name) {
					return errors.New("unselected SecretStore path")
				}
				return nil
			})
		case "volume":
			err = WalkArchive(path, e.Config.MaxArchiveBytes, nil)
			if err == nil && c.Purpose == "sqlite" {
				err = validateSQLite(path, c, e.Config.MaxArchiveBytes)
			}
		case "redis":
			err = readRedis(path, c, e.Config.MaxArchiveBytes, nil)
		case "k8s-object":
			var o map[string]any
			f, openErr := os.Open(path)
			if openErr != nil {
				return openErr
			}
			err = json.NewDecoder(f).Decode(&o)
			f.Close()
			if err == nil {
				kind := "Secret"
				if c.Resource == "configmap" {
					kind = "ConfigMap"
				}
				if o["kind"] != kind || o["apiVersion"] != "v1" {
					err = errors.New("runtime object kind mismatch")
				}
			}
		case "postgres":
			if c.Database != "@globals" {
				f, openErr := os.Open(path)
				if openErr != nil {
					return openErr
				}
				err = e.pod(ctx, c, f, io.Discard, "pg_restore", "--list")
				f.Close()
			} else {
				b, readErr := os.ReadFile(path)
				err = readErr
				if err == nil && !bytes.Contains(b, []byte("PostgreSQL database cluster dump")) {
					err = errors.New("invalid PostgreSQL globals dump")
				}
			}
		}
		if err != nil {
			return fmt.Errorf("validate %s failed: %w", c.ID, err)
		}
	}
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return errors.New("unlisted component artifact")
		}
	}
	return nil
}

func (e *Engine) releaseInventory(ctx context.Context) (map[string][]string, error) {
	result := map[string][]string{}
	for _, w := range e.Config.Workloads {
		var o kubeObject
		if err := e.get(ctx, &o, "-n", w.Namespace, "get", w.Kind, w.Name); err != nil {
			return nil, err
		}
		images := []string{}
		for _, c := range o.Spec.Template.Spec.Containers {
			images = append(images, c.Name+"="+c.Image)
		}
		for _, c := range o.Spec.Template.Spec.InitContainers {
			images = append(images, "init/"+c.Name+"="+c.Image)
		}
		if len(images) == 0 {
			return nil, errors.New("workload has no image inventory")
		}
		sort.Strings(images)
		result[workloadKey(w)] = images
	}
	return result, nil
}
func selectedSecret(c Component, name string) bool {
	if !AllowedSecretPath(name) {
		return false
	}
	for _, p := range c.Paths {
		if name == p || strings.HasPrefix(name, p+"/") {
			return true
		}
	}
	return false
}

func validateSQLite(archive string, c Component, limit int64) error {
	tmp, err := os.MkdirTemp(filepath.Dir(archive), ".sqlite-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err = RestorePaths(archive, tmp, limit, func(string) bool { return true }); err != nil {
		return err
	}
	for _, name := range c.SQLiteFiles {
		path := filepath.Join(tmp, name)
		if _, err = os.Stat(path); err != nil {
			return errors.New("SQLite file missing")
		}
		db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}).String())
		if err != nil {
			return err
		}
		var result string
		err = db.QueryRow("PRAGMA integrity_check").Scan(&result)
		db.Close()
		if err != nil || result != "ok" {
			return errors.New("SQLite integrity_check failed")
		}
	}
	return nil
}

func (e *Engine) Apply(ctx context.Context, dir string) error {
	// Globals precede databases; runtime credentials and offline volumes precede
	// restarts. No application can run while this loop is partially complete.
	components := append([]Component(nil), e.Config.Components...)
	sort.SliceStable(components, func(i, j int) bool {
		return components[i].Database == "@globals" && components[j].Database != "@globals"
	})
	for _, c := range components {
		path := filepath.Join(dir, c.ID+".data")
		var err error
		switch c.Kind {
		case "secretstore":
			err = ReplaceSecretPaths(path, e.SecretRoot, c, e.Config.MaxArchiveBytes)
		case "redis":
			err = e.restoreRedis(ctx, c, path)
		case "postgres":
			if c.Database == "@globals" {
				err = e.restoreGlobals(ctx, c, path)
			} else {
				f, openErr := os.Open(path)
				if openErr != nil {
					return openErr
				}
				err = e.pod(ctx, c, f, io.Discard, "pg_restore", "-U", c.User, "--dbname", "postgres", "--clean", "--if-exists", "--create", "--exit-on-error")
				f.Close()
			}
		case "volume":
			err = e.helper(ctx, c, func(helper Component) error {
				// Only this inventoried, unmounted PVC is touched. find does not follow
				// links or cross filesystems. Archive validation has already rejected links.
				if err := e.pod(ctx, helper, nil, io.Discard, "find", "/backup", "-xdev", "-mindepth", "1", "-delete"); err != nil {
					return err
				}
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				return e.pod(ctx, helper, f, io.Discard, "tar", "-xpf", "-", "-C", "/backup")
			})
		case "k8s-object":
			var o map[string]any
			b, readErr := os.ReadFile(path)
			err = readErr
			if err == nil {
				err = json.Unmarshal(b, &o)
			}
			if err == nil {
				sanitizeObject(o, c)
				b, _ = json.Marshal(o)
				err = e.kube(ctx, bytes.NewReader(b), io.Discard, "-n", c.Namespace, "apply", "-f", "-")
			}
		}
		if err != nil {
			return fmt.Errorf("restore %s failed; target remains in maintenance: %w", c.ID, err)
		}
	}
	return nil
}

func (e *Engine) restoreGlobals(ctx context.Context, c Component, path string) error {
	b, err := e.podOutput(ctx, c, "psql", "-X", "-U", c.User, "-d", "postgres", "-At", "-v", "ON_ERROR_STOP=1", "-c", "SELECT rolname FROM pg_roles")
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, name := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		existing[name] = true
	}
	dump, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(dump), "\n")
	create := regexp.MustCompile(`^CREATE ROLE ([a-zA-Z_][a-zA-Z0-9_]*);$`)
	for i, line := range lines {
		if strings.HasPrefix(line, "CREATE ROLE ") {
			match := create.FindStringSubmatch(line)
			if match == nil {
				return errors.New("quoted/custom role names require reviewed globals adapter")
			}
			if existing[match[1]] {
				lines[i] = ""
			}
		}
	}
	return e.pod(ctx, c, strings.NewReader(strings.Join(lines, "\n")), io.Discard, "psql", "-X", "-U", c.User, "-d", "postgres", "-v", "ON_ERROR_STOP=1")
}
