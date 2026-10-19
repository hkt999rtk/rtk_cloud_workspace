package main

import "strings"

const managedLateOriginalVolumePrefix = "accepted-service-core-managed-partial-original-volume-"
const managedLateNewTrustVolumePrefix = "accepted-service-core-managed-application-new-trust-volume-"

// The six original PVC owners and two accepted trust allocations are distinct
// from the original ledger and active-ledger metadata. Their native provenance
// is checked by the existing application/all18 volume observers.
func managedLateOriginalVolumePins(materials map[string]string) (map[string]string, error) {
	pins := map[string]string{}
	original, allocated := 0, 0
	for key, value := range materials {
		prefix := ""
		switch {
		case key == managedLateOriginalVolumePrefix+"ledger" || key == managedLateOriginalVolumePrefix+"ledger-active":
			continue
		case strings.HasPrefix(key, managedLateOriginalVolumePrefix):
			prefix = managedLateOriginalVolumePrefix
			original++
		case strings.HasPrefix(key, managedLateNewTrustVolumePrefix):
			prefix = managedLateNewTrustVolumePrefix
			allocated++
		default:
			continue
		}
		id := strings.TrimPrefix(key, prefix)
		parts := strings.Split(id, "/")
		if len(parts) != 3 || parts[0] != "PersistentVolumeClaim" || !environmentCreateConsumerID.MatchString(parts[1]) || !environmentCreateConsumerID.MatchString(parts[2]) || !environmentPreflightDigest.MatchString(value) {
			return nil, createOpenBaoACKBlock("original-volume-proof")
		}
		pins[key] = value
	}
	if original != 6 || allocated != 2 || len(pins) != 8 {
		return nil, createOpenBaoACKBlock("original-volume-proof")
	}
	return pins, nil
}
