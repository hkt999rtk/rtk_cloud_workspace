package main

import (
	"maps"
	"strings"
	"testing"
)

func TestFirstManagedMaterialMismatchKeyDeterministic(t *testing.T) {
	const first = "accepted-service-core-late-session-custody-plan"
	const last = "accepted-service-core-managed-late-current-session"
	expected := map[string]string{first: "original-preview-canary", last: "original-native-canary", "accepted-service-core-expected-only": "retained"}
	for _, reversed := range []bool{false, true} {
		observed := map[string]string{}
		if reversed {
			observed[last] = "current-native-canary"
			observed[first] = "current-preview-canary"
		} else {
			observed[first] = "current-preview-canary"
			observed[last] = "current-native-canary"
		}
		originalExpected, originalObserved := maps.Clone(expected), maps.Clone(observed)
		for range 100 {
			if got := firstManagedMaterialMismatchKey(expected, observed); got != first {
				t.Fatalf("first mismatch = %q; want the maintained preview key", got)
			}
		}
		if !maps.Equal(expected, originalExpected) || !maps.Equal(observed, originalObserved) {
			t.Fatal("diagnostic mutated its input evidence")
		}
	}
}

func TestFirstManagedMaterialMismatchKeyObservedOnly(t *testing.T) {
	const key = "accepted-service-core-managed-active-registry"
	for _, test := range []struct {
		name               string
		expected, observed map[string]string
		want               string
	}{
		{name: "empty"},
		{name: "equal", expected: map[string]string{key: "same"}, observed: map[string]string{key: "same"}},
		{name: "expected-only", expected: map[string]string{key: "retained"}},
		{name: "missing-expected", observed: map[string]string{key: "native-evidence-canary"}, want: key},
		{name: "unchanged-empty-value-semantics", observed: map[string]string{key: ""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := firstManagedMaterialMismatchKey(test.expected, test.observed); got != test.want {
				t.Fatalf("mismatch key = %q; want %q", got, test.want)
			}
		})
	}
}

func TestFirstManagedMaterialMismatchKeyDoesNotExposeUnmaintainedNames(t *testing.T) {
	for _, key := range []string{
		"private-token-canary",
		"accepted-service-core-",
		"accepted-service-core-Uppercase",
		"accepted-service-core-private/token-canary",
		"accepted-service-core-private\nvalue-canary",
		"accepted-service-core-private\x00value-canary",
		"accepted-service-core-私密",
		"accepted-service-core--empty-segment",
		"accepted-service-core-trailing-",
		"accepted-service-core-" + strings.Repeat("a", 160),
	} {
		if got := firstManagedMaterialMismatchKey(nil, map[string]string{key: "secret-value-canary"}); got != "unrecognized-material-key" {
			t.Fatal("unsafe diagnostic identifier was not replaced")
		}
	}
	// Equal keys are never reported, including unknown legacy keys.
	if got := firstManagedMaterialMismatchKey(map[string]string{"private-token-canary": "same"}, map[string]string{"private-token-canary": "same"}); got != "" {
		t.Fatal("equal evidence produced a diagnostic")
	}
}
