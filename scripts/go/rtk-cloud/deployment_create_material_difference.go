package main

import (
	"sort"
	"strings"
)

// firstManagedMaterialMismatchKey preserves the observed-only comparison used by
// the native core gate. It exposes an identifier, never either material value.
func firstManagedMaterialMismatchKey(expected, observed map[string]string) string {
	keys := make([]string, 0, len(observed))
	for key := range observed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if expected[key] == observed[key] {
			continue
		}
		const prefix = "accepted-service-core-"
		if len(key) <= len(prefix) || len(key) > 160 || !strings.HasPrefix(key, prefix) {
			return "unrecognized-material-key"
		}
		previousHyphen := true
		for _, character := range key[len(prefix):] {
			if character == '-' {
				if previousHyphen {
					return "unrecognized-material-key"
				}
				previousHyphen = true
			} else if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
				previousHyphen = false
			} else {
				return "unrecognized-material-key"
			}
		}
		if previousHyphen {
			return "unrecognized-material-key"
		}
		return key
	}
	return ""
}
