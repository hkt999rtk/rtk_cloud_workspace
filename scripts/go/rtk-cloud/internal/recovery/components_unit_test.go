package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Exercise the actual adapter without depending on a running Redis, Kubernetes,
// credentials or opt-in integration fixtures on the PR runner.
func TestRedisAdapterWithoutExternalServices(t *testing.T) {
	ctx := context.Background()
	c := Component{ID: "shadow", Kind: "redis", Namespace: "platform", Pod: "redis-0", Container: "redis", Prefixes: []string{"shadow:"}, ExcludeKeys: []string{"shadow:lease"}}
	dump := []byte("binary\x00\xffserialized-value")
	values := map[string][]byte{"shadow:doc": dump, "shadow:lease": []byte("keep lease"), "cache:item": []byte("keep cache")}
	e := &Engine{Config: Config{MaxArchiveBytes: 1 << 20, TimeoutSeconds: 1, Components: []Component{c}}}
	e.Exec = func(_ context.Context, argv []string, in io.Reader, out io.Writer) error {
		switch {
		case slices.Contains(argv, "SCAN"):
			keys := []string{}
			for k := range values {
				if strings.HasPrefix(k, "shadow:") {
					keys = append(keys, k)
				}
			}
			return json.NewEncoder(out).Encode([]any{"0", keys})
		case slices.Contains(argv, "PTTL"):
			_, err := io.WriteString(out, "-1\n")
			return err
		case slices.Contains(argv, "DUMP"):
			_, err := out.Write(values[argv[len(argv)-1]])
			return err
		case slices.Contains(argv, "DEL"):
			delete(values, argv[len(argv)-1])
			return nil
		case slices.Contains(argv, "RESTORE"):
			b, err := io.ReadAll(in)
			if err != nil {
				return err
			}
			values[argv[len(argv)-2]] = b
			_, err = io.WriteString(out, "OK\n")
			return err
		default:
			t.Fatalf("unexpected command %v", argv)
			return nil
		}
	}
	dir := privateTemp(t)
	if err := e.Capture(ctx, dir); err != nil {
		t.Fatal(err)
	}
	values["shadow:doc"] = []byte("changed")
	values["shadow:stale"] = dump
	if err := e.Apply(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(values["shadow:doc"], dump) || values["shadow:stale"] != nil || string(values["cache:item"]) != "keep cache" || string(values["shadow:lease"]) != "keep lease" {
		t.Fatalf("wrong restore scope: %v", values)
	}
	if err := readRedis(filepath.Join(dir, "shadow.data"), c, 1<<20, func(redisEntry) error { return errors.New("visitor failed") }); err == nil {
		t.Fatal("ignored visitor failure")
	}
}

func TestRedisRejectsInvalidResponsesAndArchives(t *testing.T) {
	ctx := context.Background()
	c := Component{Prefixes: []string{"shadow:"}, ExcludeKeys: []string{"shadow:lease"}}
	for _, reply := range []string{`invalid`, `[]`, `[0,[]]`, `["x",[]]`, `["0",{}]`, `["0",["cache:key"]]`, `["0",["shadow:bad\nkey"]]`} {
		t.Run(reply, func(t *testing.T) {
			e := Engine{Config: Config{MaxArchiveBytes: 4096}, Exec: func(_ context.Context, _ []string, _ io.Reader, out io.Writer) error {
				_, err := io.WriteString(out, reply)
				return err
			}}
			if _, err := e.redisKeys(ctx, c); err == nil {
				t.Fatal("accepted unsafe scan response")
			}
		})
	}
	for _, failure := range []string{"SCAN", "PTTL", "DUMP", "ttl", "short", "write"} {
		t.Run(failure, func(t *testing.T) {
			e := Engine{Config: Config{MaxArchiveBytes: 4096}, Exec: func(_ context.Context, argv []string, _ io.Reader, out io.Writer) error {
				if slices.Contains(argv, failure) {
					return errors.New("fixture failure")
				}
				s := "0123456789binary"
				if slices.Contains(argv, "SCAN") {
					s = `["0",["shadow:key"]]`
				}
				if slices.Contains(argv, "PTTL") {
					s = "-1"
					if failure == "ttl" {
						s = "100"
					}
				}
				if slices.Contains(argv, "DUMP") && failure == "short" {
					s = "bad"
				}
				_, err := io.WriteString(out, s)
				return err
			}}
			var out io.Writer = io.Discard
			if failure == "write" {
				out = &boundedWriter{writer: io.Discard, remaining: 1}
			}
			if err := e.captureRedis(ctx, c, out); err == nil {
				t.Fatal("capture ignored failure")
			}
		})
	}
	valid, _ := json.Marshal(redisEntry{Key: "shadow:key", Dump: []byte("0123456789binary")})
	for _, content := range []string{`{`, `{"extra":true}`, `{"key":"cache:key","dump":"MDEyMzQ1Njc4OWJpbmFyeQ=="}`, `{"key":"shadow:lease","dump":"MDEyMzQ1Njc4OWJpbmFyeQ=="}`, `{"key":"shadow:key","dump":"eA=="}`, string(valid) + "\n" + string(valid)} {
		p := filepath.Join(privateTemp(t), "redis")
		writeTest(t, p, []byte(content))
		if err := readRedis(p, c, 4096, nil); err == nil {
			t.Fatalf("accepted bad archive %s", content)
		}
	}
	if err := readRedis(filepath.Join(privateTemp(t), "missing"), c, 4096, nil); err == nil {
		t.Fatal("accepted missing archive")
	}
	for _, failure := range []string{"SCAN", "DEL", "RESTORE", "reply"} {
		t.Run("restore-"+failure, func(t *testing.T) {
			p := filepath.Join(privateTemp(t), "redis")
			writeTest(t, p, valid)
			e := Engine{Config: Config{MaxArchiveBytes: 4096}, Exec: func(_ context.Context, argv []string, _ io.Reader, out io.Writer) error {
				if slices.Contains(argv, failure) {
					return errors.New("fixture failure")
				}
				s := "NO"
				if slices.Contains(argv, "SCAN") {
					s = `["0",["shadow:stale"]]`
				}
				_, err := io.WriteString(out, s)
				return err
			}}
			if err := e.restoreRedis(ctx, c, p); err == nil {
				t.Fatal("restore ignored failure")
			}
		})
	}
}

func TestRuntimeObjectsAreSanitizedAndValidated(t *testing.T) {
	for _, resource := range []string{"secret", "configmap"} {
		t.Run(resource, func(t *testing.T) {
			kind := "Secret"
			if resource == "configmap" {
				kind = "ConfigMap"
			}
			c := Component{ID: "runtime", Kind: "k8s-object", Namespace: "target", Resource: resource, ResourceName: "runtime"}
			e := Engine{Config: Config{MaxArchiveBytes: 4096, Components: []Component{c}}, Exec: func(_ context.Context, argv []string, in io.Reader, out io.Writer) error {
				if slices.Contains(argv, "get") {
					return json.NewEncoder(out).Encode(map[string]any{"apiVersion": "v1", "kind": kind, "metadata": map[string]any{"uid": "old", "namespace": "source"}, "status": map[string]any{}, "data": map[string]string{"key": "dmFsdWU="}})
				}
				var obj map[string]any
				if err := json.NewDecoder(in).Decode(&obj); err != nil {
					return err
				}
				meta := obj["metadata"].(map[string]any)
				if meta["namespace"] != "target" || meta["name"] != "runtime" || meta["uid"] != nil || obj["status"] != nil {
					t.Fatalf("source metadata survived: %v", obj)
				}
				return nil
			}}
			dir := privateTemp(t)
			if err := e.Capture(context.Background(), dir); err != nil {
				t.Fatal(err)
			}
			if err := e.Apply(context.Background(), dir); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, "runtime.data")
			writeTest(t, p, []byte(`{"kind":"Pod","apiVersion":"v1"}`))
			if e.ValidateArtifacts(context.Background(), dir) == nil {
				t.Fatal("accepted wrong object kind")
			}
			writeTest(t, p, []byte(`{`))
			if e.Apply(context.Background(), dir) == nil {
				t.Fatal("applied corrupt JSON")
			}
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if e.Apply(context.Background(), dir) == nil {
				t.Fatal("applied missing object")
			}
		})
	}
}
