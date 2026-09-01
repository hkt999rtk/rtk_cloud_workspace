package recovery

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigInventoryValidation(t *testing.T) {
	for name, change := range map[string]func(*Config){
		"version":             func(c *Config) { c.Version++ },
		"timeout":             func(c *Config) { c.TimeoutSeconds = 0 },
		"namespaces":          func(c *Config) { c.Namespaces = append(c.Namespaces, c.Namespaces[0]) },
		"lock":                func(c *Config) { c.LockNamespace = "missing" },
		"exclusion":           func(c *Config) { c.ExcludedPVCs = []Exclusion{{Namespace: "platform", Name: "cache"}} },
		"duplicate-workload":  func(c *Config) { c.Workloads = append(c.Workloads, c.Workloads[0]) },
		"empty-workloads":     func(c *Config) { c.Workloads = nil },
		"duplicate-component": func(c *Config) { c.Components = append(c.Components, c.Components[0]) },
		"component-namespace": func(c *Config) { c.Components[0].Namespace = "missing" },
		"postgres-user":       func(c *Config) { c.Components[0].User = "bad-user" },
		"postgres-system":     func(c *Config) { c.Components[1].Database = "postgres" },
		"postgres-duplicate":  func(c *Config) { v := c.Components[0]; v.ID = "another"; c.Components = append(c.Components, v) },
		"volume-purpose":      func(c *Config) { c.Components[2].Purpose = "raft" },
		"sqlite-path":         func(c *Config) { c.Components[2].SQLiteFiles = []string{"../escape"} },
		"sqlite-inventory":    func(c *Config) { c.Components[2].Purpose = "sqlite" },
		"secret-paths":        func(c *Config) { c.Components[3].Paths = nil },
		"kind":                func(c *Config) { c.Components[0].Kind = "unknown" },
		"check":               func(c *Config) { c.HealthChecks = []Check{{ID: "check"}} },
		"dependencies":        func(c *Config) { c.ExternalDependencies = nil },
		"redis-pod": func(c *Config) {
			c.Components = append(c.Components, Component{ID: "redis", Kind: "redis", Namespace: "platform"})
		},
		"redis-prefix": func(c *Config) {
			c.Components = append(c.Components, Component{ID: "redis", Kind: "redis", Namespace: "platform", Pod: "redis-0", Prefixes: []string{"*"}})
		},
		"redis-exclusion": func(c *Config) {
			c.Components = append(c.Components, Component{ID: "redis", Kind: "redis", Namespace: "platform", Pod: "redis-0", Prefixes: []string{"shadow:"}, ExcludeKeys: []string{"outside"}})
		},
		"object": func(c *Config) {
			c.Components = append(c.Components, Component{ID: "object", Kind: "k8s-object", Namespace: "platform", Resource: "pod", ResourceName: "bad"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := fixture(t)
			change(&c)
			if c.Validate() == nil {
				t.Fatal("accepted invalid inventory")
			}
		})
	}
	c := fixture(t)
	c.ExcludedPVCs = []Exclusion{{Namespace: "platform", Name: "cache", Reason: "disposable"}}
	c.Components = append(c.Components, Component{ID: "redis", Kind: "redis", Namespace: "platform", Pod: "redis-0", Prefixes: []string{"shadow:"}, ExcludeKeys: []string{"shadow:lease"}}, Component{ID: "object", Kind: "k8s-object", Namespace: "platform", Resource: "secret", ResourceName: "runtime"})
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := Decode(strings.NewReader(`{} {}`), new(Config)); err == nil {
		t.Fatal("accepted trailing JSON")
	}
}

func TestPrivateDirectoriesAndBoundedIO(t *testing.T) {
	root := privateTemp(t)
	if err := PrivateDirectory(filepath.Join(root, "new", "directory")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "public"), 0755); err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(root, "file"), []byte("x"))
	if err := os.Symlink(root, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"relative", filepath.Join(root, "file", "child"), filepath.Join(root, "link", "child"), filepath.Join(root, "public")} {
		if PrivateDirectory(p) == nil {
			t.Fatalf("accepted unsafe directory %s", p)
		}
	}
	if _, err := DigestFile(filepath.Join(root, "missing")); err == nil {
		t.Fatal("accepted absent digest file")
	}
	if err := WriteJSON(filepath.Join(root, "missing", "json"), true); err == nil {
		t.Fatal("ignored write failure")
	}
	if err := WriteJSON(filepath.Join(root, "json"), make(chan int)); err == nil {
		t.Fatal("ignored encoding failure")
	}
	b := limitedBuffer{limit: 1}
	if _, err := b.Write([]byte("xx")); err == nil {
		t.Fatal("unbounded subprocess output")
	}
	if err := QuietExec(context.Background(), nil, nil, io.Discard); err == nil {
		t.Fatal("accepted empty command")
	}
	e := Engine{Config: Config{TimeoutSeconds: 1}}
	if err := e.command(context.Background(), []string{"true"}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e.poll(ctx, func() (bool, error) { return false, nil }) == nil {
		t.Fatal("ignored canceled wait")
	}
	if e.poll(ctx, func() (bool, error) { return false, errors.New("failed") }) == nil {
		t.Fatal("ignored failed wait")
	}
}

func TestRemoteCredentialsNeverFallBackAndWrappersValidate(t *testing.T) {
	t.Setenv("RTK_BACKUP_ACCESS_KEY_ID", "")
	t.Setenv("RTK_BACKUP_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "unrelated-fixture")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "unrelated-fixture")
	c := fixture(t)
	if _, err := RemoteClient(c.Remote); err == nil {
		t.Fatal("used unrelated AWS credentials")
	}
	if Upload(context.Background(), c, "backup", "missing") == nil {
		t.Fatal("upload without credentials")
	}
	if _, err := Download(context.Background(), c, "backup", c.Directory); err == nil {
		t.Fatal("download without credentials")
	}
	for _, id := range []string{"../escape", ""} {
		if Upload(context.Background(), c, id, "missing") == nil {
			t.Fatal("invalid upload id")
		}
		if _, err := Download(context.Background(), c, id, c.Directory); err == nil {
			t.Fatal("invalid download id")
		}
	}
	t.Setenv("RTK_BACKUP_ACCESS_KEY_ID", "backup-fixture")
	t.Setenv("RTK_BACKUP_SECRET_ACCESS_KEY", "backup-fixture-secret")
	t.Setenv("RTK_BACKUP_SESSION_TOKEN", "backup-fixture-token")
	client, err := RemoteClient(c.Remote)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := client.Options().Credentials.Retrieve(context.Background())
	if err != nil || credentials.AccessKeyID != "backup-fixture" || credentials.SessionToken != "backup-fixture-token" {
		t.Fatal("dedicated credentials not used")
	}
	if Upload(context.Background(), c, "backup", "missing") == nil {
		t.Fatal("accepted missing file")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Download(ctx, c, "backup", c.Directory); err == nil {
		t.Fatal("ignored cancellation")
	}
}
