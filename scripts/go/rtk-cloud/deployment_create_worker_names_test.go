package main

import (
	"math"
	"strings"
	"testing"
)

func TestWorkerNamingCarriesEnvironmentWithoutOpaqueSuffix(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		name, err := environmentWorkerDisplayName("video-cloud-"+environment, "general", 1)
		if err != nil || name != "video-cloud-"+environment+"-lke-general-01" {
			t.Fatal(name, err)
		}
	}
	for _, input := range []struct {
		stack, class string
		ordinal      int
	}{
		{"video-cloud-dev", "unknown", 1}, {"video-cloud-dev", "general", 0},
		{"video-cloud-dev", "general", 10000}, {strings.Repeat("x", 64), "general", 1},
	} {
		if _, err := environmentWorkerDisplayName(input.stack, input.class, input.ordinal); err == nil {
			t.Fatal("invalid naming accepted")
		}
	}
}

func TestWorkerIDsRejectLossyNativeValues(t *testing.T) {
	for _, value := range []any{nil, "107939721", -1.0, 0.0, 1.5, math.NaN(), math.Inf(1), 9007199254740992.0} {
		if workerNativeIDNumber(value) != 0 {
			t.Fatal("invalid native identity accepted")
		}
	}
	if workerNativeIDNumber(107939721.0) != 107939721 {
		t.Fatal("original native ID changed")
	}
}
