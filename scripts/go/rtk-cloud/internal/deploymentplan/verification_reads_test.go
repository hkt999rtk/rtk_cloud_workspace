package deploymentplan

import (
	"context"
	"fmt"
	"os"
	"testing"
)

func TestFinalVerificationReadsEachOwnerOnce(t *testing.T) {
	for _, mode := range []string{"create", "update", "retain"} {
		t.Run(mode, func(t *testing.T) {
			in, d, live := fixture(t, "staging", "Deployment")
			objects := map[string]Object{}
			var inventory []Object
			if mode != "create" {
				inventory = []Object{live}
				objects[ID(live)] = Clone(live)
			}
			if mode == "update" {
				Map(d.Object["spec"])["replicas"] = float64(2)
			}
			p, err := Compile(in, []Definition{d}, inventory)
			if err != nil {
				t.Fatal(err)
			}
			b := &phaseReadBackend{memoryBackend: memoryBackend{objects: objects}}
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			check := func(context.Context, *Plan) error { return nil }
			readsAtAcceptance := 0
			accept := func(context.Context, *Plan) error { readsAtAcceptance = len(b.reads); return nil }
			if err := p.Execute(t.Context(), b, p.SHA256(), root, check, check, accept); err != nil {
				t.Fatal(err)
			}
			if got := len(b.reads) - readsAtAcceptance; got != 1 {
				t.Fatalf("post-acceptance reads = %d, want one combined content/owner observation", got)
			}
		})
	}
}

// Compare the former separate GET loops with the combined verification using
// the same in-memory backend. This measures local work and read counts, not
// Kubernetes network latency or end-to-end deployment duration.
func BenchmarkFinalVerification287Resources(b *testing.B) {
	p := &Plan{}
	objects := map[string]Object{}
	owners := map[string]string{}
	for i := 0; i < 287; i++ {
		object := Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": Object{"name": fmt.Sprintf("resource-%d", i), "namespace": "fixture", "uid": fmt.Sprintf("owner-%d", i)}, "data": Object{"value": "original"}}
		id := ID(object)
		objects[id] = object
		owners[id] = String(Map(object["metadata"])["uid"])
		p.Resources = append(p.Resources, Resource{ID: id, Action: "create", Desired: Projection(object)})
	}
	for _, mode := range []string{"separate", "combined"} {
		b.Run(mode, func(b *testing.B) {
			backend := &classifiedReadBackend{memoryBackend: memoryBackend{objects: objects}}
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if mode == "combined" {
					if err := p.verify(ctx, backend, owners); err != nil {
						b.Fatal(err)
					}
					continue
				}
				if err := p.Verify(ctx, backend); err != nil {
					b.Fatal(err)
				}
				for _, r := range p.Resources {
					current, err := backend.Read(ctx, r.ID)
					if err != nil || String(Map(current["metadata"])["uid"]) != owners[r.ID] {
						b.Fatal("owner changed", err)
					}
				}
			}
			b.ReportMetric(float64(backend.reads)/float64(b.N), "reads/op")
		})
	}
}
