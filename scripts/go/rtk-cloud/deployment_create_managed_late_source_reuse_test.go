package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedLateSourceReuseStreamsOriginalExecutionArtifactBound(t *testing.T) {
	store, e, _, _ := managedLateSourceReuseNegativeFixture(t)
	relative := "deployment/rollback/" + strings.Repeat("d", 64) + "/plan.json"
	raw := bytes.Repeat([]byte("isolated-original-plan"), (5<<20)/len("isolated-original-plan"))
	managedLateSourceReuseNegativeWrite(t, store, relative, raw)
	reuse, err := newManagedLateSourceReuse(store, e)
	if err != nil || reuse.manifest[relative] != environmentPreflightBytesSHA256(raw) {
		t.Fatal("genuine bounded execution evidence over the smaller input-file limit refused", err)
	}
	e.lateSourceReuse = reuse
	if err := checkManagedLateSourceReuse(e); err != nil {
		t.Fatal("unchanged streamed original evidence refused", err)
	}
	file, err := os.OpenFile(filepath.Join(store.Root, relative), os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(environmentPreflightArtifactLimit + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := newManagedLateSourceReuse(store, e); err == nil {
		t.Fatal("source manifest accepted evidence beyond the maintained execution bound")
	}
}

func TestManagedLateSourceReuseOnePureDerivationPerCommandPhase(t *testing.T) {
	store, e, cfg, _ := managedLateSourceReuseNegativeFixture(t)
	derivations, liveReads := 0, 0
	loadHistory := func() map[string]string {
		var history map[string]string
		hit, err := readManagedLateSourceReuse(store, e, cfg, "isolated-successful-pure-constructor", &history)
		if err != nil {
			t.Fatal(err)
		}
		if !hit {
			derivations++
			history = map[string]string{"verified-history": "original"}
			if err := rememberManagedLateSourceReuse(store, e, cfg, "isolated-successful-pure-constructor", history); err != nil {
				t.Fatal(err)
			}
		}
		return history
	}
	for range 20 {
		history := loadHistory()
		if history["verified-history"] != "original" {
			t.Fatal("retained pure source was modified")
		}
		history["verified-history"] = "caller-only-mutation"
		// Native/current-child observations stay outside the pure memo seam.
		liveReads++
	}
	if derivations != 1 || liveReads != 20 {
		t.Fatalf("want one pure derivation and twenty live boundaries, got %d/%d", derivations, liveReads)
	}
	invalidateManagedLateSourceReuse(e)
	if managedLateSourceReuseActive(e) {
		t.Fatal("mutation boundary retained memo authority")
	}
	reuse, err := newManagedLateSourceReuse(store, e)
	if err != nil {
		t.Fatal(err)
	}
	e.lateSourceReuse = reuse
	loadHistory()
	if derivations != 2 {
		t.Fatal("new command phase reused the prior phase's authenticated result")
	}
}
