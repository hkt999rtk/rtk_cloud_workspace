package main

import (
	"context"
	"fmt"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"testing"
	"time"
)

func redisDurableProjectionFixture() (environmentCreateJournal, environmentCreateJournal) {
	now := time.Now().UTC().Add(-time.Hour)
	source := environmentCreateJournal{PlanSHA256: strings.Repeat("a", 64), Stack: "video-cloud-dev", Environment: "dev", Entries: make([]environmentCreateJournalEntry, 434)}
	for i := range source.Entries {
		at := now.Add(time.Duration(i) * time.Millisecond)
		source.Entries[i] = environmentCreateJournalEntry{Key: fmt.Sprint("original-", i), Phase: "bootstrap", State: "completed", StartedAt: at, CompletedAt: at.Add(time.Millisecond), IntentSHA256: strings.Repeat("b", 64), Receipt: []byte(`{"original":true}`)}
	}
	name := "retire-pki-bootstrap-custody-" + source.PlanSHA256[:12]
	for i, key := range []string{"kubernetes/account-manager-registration", name, "kubernetes/" + name, environmentCreateIngressAdmissionEntry, "kubernetes/ingress-admission-identity", "kubernetes/ingress-controller"} {
		source.Entries[428+i].Key = key
	}
	source.Entries[431].Phase = "ingress-admission"
	source.Entries[431].CompletedAt = now.Add(time.Minute)
	source.Entries[433].Phase = "ingress-controller"
	current := source
	current.Entries = slices.Clone(source.Entries)
	keys := []string{"normal-redis-durable", "kubernetes/normal-redis-durable-claim", "kubernetes/normal-redis-durable-target", "normal-redis-durable-snapshot", "normal-redis-durable-target-verified", "kubernetes/normal-redis-durable-retire", "kubernetes/normal-redis-durable-stop", "kubernetes/normal-redis-durable-switch", "normal-redis-durable-verified"}
	for i, key := range keys {
		at := now.Add(2*time.Minute + time.Duration(i)*time.Second)
		entry := environmentCreateJournalEntry{Key: key, Phase: "redis-durable", State: "completed", StartedAt: at, CompletedAt: at.Add(time.Millisecond), IntentSHA256: strings.Repeat("c", 64), Receipt: []byte(`{"proof":true}`)}
		if strings.HasPrefix(key, "kubernetes/") {
			entry.Phase = strings.TrimPrefix(key, "kubernetes/")
			sha := deploymentplan.Digest(key)
			entry.Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": sha, "owners": map[string]string{"resource": "11111111-1111-4111-8111-111111111111"}})
			current.KubernetesLeases = append(current.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/video-cloud-dev-video-cloud/rtk-deployment-execution", UID: fmt.Sprintf("%08d-1111-4111-8111-111111111111", i+1), Holder: sha + ":owned"})
		}
		current.Entries = append(current.Entries, entry)
	}
	current.Entries[434].CompletedAt = current.Entries[442].CompletedAt.Add(time.Millisecond)
	return source, current
}
func TestManagedLateRedisDurableSourceProjectionFreezesOriginal434(t *testing.T) {
	source, current := redisDurableProjectionFixture()
	before := deploymentplan.Digest(current)
	got, err := managedLateRedisDurableSourcePrefix(source, current)
	if err != nil || deploymentplan.Digest(got) != deploymentplan.Digest(source) {
		t.Fatal("genuine ordered suffix rejected", err)
	}
	for n := 435; n <= 443; n++ {
		partial := current
		partial.Entries = slices.Clone(current.Entries[:n])
		partial.Entries[434].State = "pending"
		partial.Entries[434].CompletedAt = time.Time{}
		partial.Entries[434].Receipt = nil
		leases := 0
		for _, e := range partial.Entries[434:] {
			if strings.HasPrefix(e.Key, "kubernetes/") {
				leases++
			}
		}
		partial.KubernetesLeases = slices.Clone(current.KubernetesLeases[:leases])
		if _, err := managedLateRedisDurableSourcePrefix(source, partial); err != nil {
			t.Fatal("known completed prefix rejected", n, err)
		}
	}
	active := current
	active.Entries = slices.Clone(current.Entries[:438])
	active.Entries[434].State, active.Entries[437].State = "pending", "pending"
	active.Entries[434].CompletedAt, active.Entries[437].CompletedAt = time.Time{}, time.Time{}
	active.Entries[434].Receipt, active.Entries[437].Receipt = nil, nil
	active.KubernetesLeases = slices.Clone(current.KubernetesLeases[:2])
	if _, err := managedLateRedisDurableSourcePrefix(source, active); err != nil {
		t.Fatal("active local snapshot stage refused", err)
	}
	for _, mode := range []string{"source", "unknown", "pending-inner", "future", "lease", "extra", "early-outer", "metadata"} {
		t.Run(mode, func(t *testing.T) {
			j := current
			j.Entries = slices.Clone(current.Entries)
			j.KubernetesLeases = slices.Clone(current.KubernetesLeases)
			switch mode {
			case "source":
				j.Entries[10].Receipt = []byte(`{}`)
			case "unknown":
				j.Entries[439].Key = "foreign"
			case "pending-inner":
				j.Entries[436].State = "pending"
			case "future":
				j.Entries[442].CompletedAt = time.Now().Add(time.Minute)
			case "lease":
				j.KubernetesLeases[0].Holder = strings.Repeat("f", 64) + ":foreign"
			case "extra":
				j.Entries = append(j.Entries, j.Entries[442])
			case "early-outer":
				j.Entries[434].CompletedAt = j.Entries[435].CompletedAt
			case "metadata":
				j.OwnerToken = "foreign"
			}
			if _, err := managedLateRedisDurableSourcePrefix(source, j); err == nil {
				t.Fatal("changed projection accepted")
			}
		})
	}
	if before != deploymentplan.Digest(current) {
		t.Fatal("projection changed actual journal")
	}
}
func TestManagedLateRedisDurableVolumeProjectionPreservesExactlyOriginal18(t *testing.T) {
	ledger := environmentCreateResourceLedger{Stack: "video-cloud-dev"}
	original := map[string]environmentCreateVolumeOwner{}
	for i := 1; i <= 18; i++ {
		o := environmentCreateVolumeOwner{NativeID: fmt.Sprint(i), PVName: fmt.Sprint("pv-", i), PVUID: fmt.Sprintf("%08d-1111-4111-8111-111111111111", i), PVCName: fmt.Sprint("claim-", i), PVCUID: fmt.Sprintf("%08d-2222-4222-8222-222222222222", i), Namespace: "video-cloud-dev-video-cloud"}
		original["PersistentVolumeClaim/"+o.Namespace+"/"+o.PVCName] = o
		ledger.Volumes = append(ledger.Volumes, o)
	}
	redis := environmentCreateVolumeOwner{NativeID: "99", PVName: "pv-redis", PVUID: "99999999-1111-4111-8111-111111111111", PVCName: "redis-data", PVCUID: "99999999-2222-4222-8222-222222222222", Namespace: "video-cloud-dev-platform"}
	before := deploymentplan.Digest(ledger)
	if got, err := managedLateRedisDurableVolumeProjection(ledger, original, nil); err != nil || len(got.Volumes) != 18 {
		t.Fatal(err)
	}
	ledger.Volumes = append(ledger.Volumes, redis)
	full := deploymentplan.Digest(ledger)
	if got, err := managedLateRedisDurableVolumeProjection(ledger, original, &redis); err != nil || deploymentplan.Digest(got.Volumes) != deploymentplan.Digest(ledger.Volumes[:18]) {
		t.Fatal("typed nineteenth claim rejected", err)
	}
	for _, mode := range []string{"unproved", "foreign-extra", "old-uid", "missing-old", "extra", "native-alias", "wrong-new-uid"} {
		t.Run(mode, func(t *testing.T) {
			l := ledger
			l.Volumes = slices.Clone(ledger.Volumes)
			r := redis
			ptr := &r
			switch mode {
			case "unproved":
				ptr = nil
			case "foreign-extra":
				l.Volumes[18].PVCName = "foreign"
			case "old-uid":
				l.Volumes[0].PVCUID = redis.PVCUID
			case "missing-old":
				l.Volumes = l.Volumes[1:]
			case "extra":
				l.Volumes = append(l.Volumes, redis)
			case "native-alias":
				l.Volumes[18].NativeID = l.Volumes[0].NativeID
				r = l.Volumes[18]
			case "wrong-new-uid":
				r.PVCUID = "88888888-2222-4222-8222-222222222222"
			}
			if _, err := managedLateRedisDurableVolumeProjection(l, original, ptr); err == nil {
				t.Fatal("unproved/changed volume accepted")
			}
		})
	}
	if full != deploymentplan.Digest(ledger) || before == full {
		t.Fatal("projection mutated ledger")
	}
}

func TestManagedLateRedisDurableHistoricalVolumeSourceRetainsActualParent(t *testing.T) {
	runtime, archived := managedLateCompletedTailAuditFixture(t)
	actual := runtime.Execution
	view, err := managedLateCompletedTailPureSourceView(context.Background(), actual, archived)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{392, 374, 361} {
		old := *view
		old.Journal = view.Journal
		old.Journal.Entries = slices.Clone(view.Journal.Entries[:n])
		if got, err := managedLateRedisVolumeActualExecution(actual.Store, &old); err != nil || got != actual {
			t.Fatal("nested pure source lost actual allocation provenance", n, err)
		}
		before := deploymentplan.Digest(old.Journal)
		for _, mode := range []string{"source", "owner", "lease", "token"} {
			changed := old
			changed.Journal = old.Journal
			changed.Journal.Entries = slices.Clone(old.Journal.Entries)
			changed.Journal.KubernetesLeases = slices.Clone(old.Journal.KubernetesLeases)
			token := *old.latePureSource
			changed.latePureSource = &token
			switch mode {
			case "source":
				changed.Journal.Entries[1].Receipt = []byte(`{}`)
			case "owner":
				changed.owner.OwnerToken = "foreign"
			case "lease":
				changed.Journal.KubernetesLeases[0].Holder = "foreign"
			case "token":
				changed.latePureSource.viewSHA = strings.Repeat("f", 64)
			}
			if _, err := managedLateRedisVolumeActualExecution(actual.Store, &changed); err == nil {
				t.Fatal("changed historical provenance accepted", n, mode)
			}
		}
		if before != deploymentplan.Digest(old.Journal) {
			t.Fatal("historical view mutated")
		}
	}
}

func TestManagedLateRedisDurableRecordedVolumePointerIsExactAndPrivate(t *testing.T) {
	source, current := redisDurableProjectionFixture()
	actual := &environmentCreateExecution{Store: secretStore{Environment: "dev", Root: t.TempDir()}, Journal: current, owner: environmentCreateOwner{OwnerToken: "same"}}
	for _, n := range []int{361, 374, 392, 434, 443} {
		recorded := *actual
		recorded.volumeExecution = actual
		recorded.Journal = current
		recorded.Journal.Entries = slices.Clone(current.Entries[:n])
		if n < 435 {
			recorded.Journal.KubernetesLeases = slices.Clone(source.KubernetesLeases)
		}
		if got, err := managedLateRedisVolumeActualExecution(actual.Store, &recorded); err != nil || got != actual {
			t.Fatal("exact admitted recorded source lost current execution", n, err)
		}
		for _, mode := range []string{"owner", "journal", "lease", "store", "cycle", "unknown-prefix"} {
			old := recorded
			old.Journal.Entries = slices.Clone(recorded.Journal.Entries)
			old.Journal.KubernetesLeases = slices.Clone(recorded.Journal.KubernetesLeases)
			switch mode {
			case "owner":
				old.owner.OwnerToken = "foreign"
			case "journal":
				old.Journal.Entries[0].Receipt = []byte(`{}`)
			case "lease":
				old.Journal.KubernetesLeases = append(old.Journal.KubernetesLeases, deploymentplan.LeaseOwner{UID: "foreign"})
			case "store":
				old.Store.Root = "foreign"
			case "cycle":
				old.volumeExecution = &old
			case "unknown-prefix":
				old.Journal.Entries = current.Entries[:435]
			}
			if _, err := managedLateRedisVolumeActualExecution(actual.Store, &old); err == nil {
				t.Fatal("foreign current volume provenance accepted", n, mode)
			}
		}
	}

	actual.Journal.CompletedAt = time.Now().UTC()
	completed := *actual
	completed.volumeExecution = actual
	if got, err := managedLateRedisVolumeActualExecution(actual.Store, &completed); err != nil || got != actual {
		t.Fatal("unchanged fully completed canonical source refused", err)
	}
	historical := completed
	historical.Journal = completed.Journal
	historical.Journal.CompletedAt = time.Time{}
	historical.Journal.Entries = slices.Clone(completed.Journal.Entries[:374])
	historical.Journal.KubernetesLeases = slices.Clone(source.KubernetesLeases)
	if got, err := managedLateRedisVolumeActualExecution(actual.Store, &historical); err != nil || got != actual {
		t.Fatal("completed canonical source lost original historical prefix", err)
	}
	historical.Journal.CompletedAt = actual.Journal.CompletedAt
	if _, err := managedLateRedisVolumeActualExecution(actual.Store, &historical); err == nil {
		t.Fatal("partial history invented overall completion")
	}
}
