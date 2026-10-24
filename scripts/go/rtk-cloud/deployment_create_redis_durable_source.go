package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"time"
)

// This parsing view never replaces the held execution used for native IO.
// Its caller independently authenticates the source and each child plan.
func managedLateRedisDurableSourcePrefix(source, current environmentCreateJournal) (environmentCreateJournal, error) {
	bad := func() (environmentCreateJournal, error) {
		return environmentCreateJournal{}, createOpenBaoACKBlock("redis-durable-source-prefix")
	}
	if len(source.Entries) != 434 || !managedLateNormalTailControllerSelected(source) || source.Entries[431].State != "completed" || !source.CompletedAt.IsZero() || !current.CompletedAt.IsZero() || len(current.Entries) < 434 || len(current.Entries) > 443 || len(current.KubernetesLeases) < len(source.KubernetesLeases) {
		return bad()
	}
	prefix := current
	prefix.Entries = slices.Clone(current.Entries[:434])
	prefix.KubernetesLeases = slices.Clone(current.KubernetesLeases[:len(source.KubernetesLeases)])
	if deploymentplan.Digest(prefix) != deploymentplan.Digest(source) {
		return bad()
	}
	keys := []string{"normal-redis-durable", "kubernetes/normal-redis-durable-claim", "kubernetes/normal-redis-durable-target", "normal-redis-durable-snapshot", "normal-redis-durable-target-verified", "kubernetes/normal-redis-durable-retire", "kubernetes/normal-redis-durable-stop", "kubernetes/normal-redis-durable-switch", "normal-redis-durable-verified"}
	var outer environmentCreateJournalEntry
	var last time.Time
	leases := 0
	seen := map[string]bool{}
	for _, lease := range source.KubernetesLeases {
		seen[lease.UID] = true
	}
	for i, entry := range current.Entries[434:] {
		phase := "redis-durable"
		kube := strings.HasPrefix(keys[i], "kubernetes/")
		if kube {
			phase = strings.TrimPrefix(keys[i], "kubernetes/")
		}
		if entry.Key != keys[i] || entry.Phase != phase || !environmentPreflightDigest.MatchString(entry.IntentSHA256) || entry.StartedAt.IsZero() || entry.StartedAt.After(time.Now().UTC()) {
			return bad()
		}
		if i == 0 {
			outer = entry
			last = entry.StartedAt
			if entry.State == "pending" {
				if !entry.CompletedAt.IsZero() || len(entry.Receipt) != 0 {
					return bad()
				}
				continue
			}
			if len(current.Entries) != 443 {
				return bad()
			}
		} else if entry.StartedAt.Before(last) || entry.StartedAt.Before(outer.StartedAt) {
			return bad()
		}
		// Native commands run inside the sole last local pending stage. A
		// Kubernetes child must already have its accepted receipt and lease.
		if i != 0 && entry.State == "pending" && !kube && i == len(current.Entries)-435 && entry.CompletedAt.IsZero() && len(entry.Receipt) == 0 && outer.State == "pending" {
			continue
		}
		if entry.State != "completed" || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(time.Now().UTC()) || len(entry.Receipt) == 0 {
			return bad()
		}
		if kube {
			var receipt struct {
				Plan   string            `json:"plan_sha256"`
				Owners map[string]string `json:"owners"`
			}
			if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || !environmentPreflightDigest.MatchString(receipt.Plan) || len(receipt.Owners) == 0 || len(current.KubernetesLeases) <= len(source.KubernetesLeases)+leases {
				return bad()
			}
			lease := current.KubernetesLeases[len(source.KubernetesLeases)+leases]
			if !managedLateLeaseMatches(lease, source.Stack, receipt.Plan) || seen[lease.UID] {
				return bad()
			}
			seen[lease.UID] = true
			leases++
		}
		if i != 0 {
			last = entry.CompletedAt
		}
	}
	if len(current.KubernetesLeases) != len(source.KubernetesLeases)+leases || outer.State == "completed" && outer.CompletedAt.Before(last) {
		return bad()
	}
	return prefix, nil
}

// Authenticate the full ledger/active pointer before this projection. The new
// owner must come from the genuine typed Redis allocation and native PV proof.
func managedLateRedisDurableVolumeProjection(ledger environmentCreateResourceLedger, original map[string]environmentCreateVolumeOwner, redis *environmentCreateVolumeOwner) (environmentCreateResourceLedger, error) {
	bad := func() (environmentCreateResourceLedger, error) {
		return environmentCreateResourceLedger{}, createOpenBaoACKBlock("redis-durable-original-volumes")
	}
	if len(original) != 18 || len(ledger.Volumes) < 18 || len(ledger.Volumes) > 19 || ledger.Stack == "" {
		return bad()
	}
	redisID := "PersistentVolumeClaim/" + ledger.Stack + "-platform/redis-data"
	if _, exists := original[redisID]; exists {
		return bad()
	}
	if redis != nil {
		if redis.Namespace != ledger.Stack+"-platform" || redis.PVCName != "redis-data" || !environmentBootstrapOwnerUUID.MatchString(redis.PVCUID) || !environmentBootstrapOwnerUUID.MatchString(redis.PVUID) || redis.PVName == "" {
			return bad()
		}
		if _, err := positiveIntValue("volume_id", redis.NativeID); err != nil {
			return bad()
		}
	}
	projected := ledger
	projected.Volumes = nil
	seen, native, pvcUIDs, pvUIDs := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, owner := range ledger.Volumes {
		id := "PersistentVolumeClaim/" + owner.Namespace + "/" + owner.PVCName
		if seen[id] || native[owner.NativeID] || pvcUIDs[owner.PVCUID] || pvUIDs[owner.PVUID] {
			return bad()
		}
		seen[id], native[owner.NativeID], pvcUIDs[owner.PVCUID], pvUIDs[owner.PVUID] = true, true, true, true
		if expected, exists := original[id]; exists {
			if expected != owner {
				return bad()
			}
			projected.Volumes = append(projected.Volumes, owner)
		} else if id != redisID || redis == nil || *redis != owner {
			return bad()
		}
	}
	if len(projected.Volumes) != 18 {
		return bad()
	}
	return projected, nil
}
