package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOriginalVolumeRemovalUsesExactNativeAbsence(t *testing.T) {
	for _, mode := range []string{"already-absent", "present-then-deleted", "delete-error-but-absent", "permission-hidden", "permission-lost-after-delete", "transport-error", "wrong-id", "attached", "delete-error-still-present"} {
		t.Run(mode, func(t *testing.T) {
			deleted, queries := 0, 0
			read := func(_ context.Context, _ string, path string, filter map[string]any) ([]byte, error) {
				queries++
				if path != "/volumes/123" || filter != nil {
					t.Fatalf("list or alternate ID cannot prove known volume absence: %s", path)
				}
				switch mode {
				case "already-absent":
					return nil, errRemovalRecoveryNativeNotFound
				case "permission-hidden", "transport-error":
					return nil, errors.New("private-provider-response-canary")
				case "wrong-id":
					return []byte(`{"id":999,"status":"active","linode_id":null}`), nil
				case "attached":
					return []byte(`{"id":123,"status":"active","linode_id":456}`), nil
				}
				if deleted > 0 {
					switch mode {
					case "present-then-deleted", "delete-error-but-absent":
						return nil, errRemovalRecoveryNativeNotFound
					case "permission-lost-after-delete":
						return nil, errors.New("private-provider-response-canary")
					}
				}
				return []byte(`{"id":123,"status":"active","linode_id":null}`), nil
			}
			remove := func(_, id string) error {
				if id != "123" {
					t.Fatal("foreign ID deletion")
				}
				deleted++
				if strings.HasPrefix(mode, "delete-error") {
					return errors.New("private-delete-response-canary")
				}
				return nil
			}
			err := deleteDeploymentVolumesWithEvidence(context.Background(), "isolated-token", []string{"123"}, 0, read, remove)
			wantSuccess := mode == "already-absent" || mode == "present-then-deleted" || mode == "delete-error-but-absent"
			if (err == nil) != wantSuccess || err != nil && strings.Contains(err.Error(), "canary") {
				t.Fatalf("known ID verification result: %v", err)
			}
			if queries == 0 || mode == "permission-hidden" && deleted != 0 {
				t.Fatalf("volume visibility failure allowed deletion: queries=%d deletes=%d", queries, deleted)
			}
		})
	}
}

func TestRemovalRecoveryNativeReadDistinguishes404From403(t *testing.T) {
	for _, status := range []string{"200", "403", "404", "500"} {
		t.Run(status, func(t *testing.T) {
			dir := t.TempDir()
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n%%s' '{\"id\":123,\"private\":\"private-response-canary\"}' '%s'\n", status)
			if status != "200" {
				script += "exit 22\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			body, err := readRemovalRecoveryNative(context.Background(), "private-authentication-canary", "/volumes/123", nil)
			if errors.Is(err, errRemovalRecoveryNativeNotFound) != (status == "404") || (err == nil) != (status == "200") || err != nil && strings.Contains(err.Error(), "canary") {
				t.Fatalf("HTTP %s classified incorrectly: %v", status, err)
			}
			if status == "200" && !strings.Contains(string(body), `"id":123`) {
				t.Fatal("native metadata body was lost")
			}
		})
	}
}
