package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCRLMaintenanceInstallerTransferUsesPinnedBytesAndCleansUp(t *testing.T) {
	for _, tampered := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing-old-binary", true: "tampered-transfer"}[tampered], func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, "state.json")
			// Transport is exercised by the actual shell. This isolated program stands
			// in for the separately validated Linux executable on a Darwin test host.
			program := []byte("#!/bin/sh\nset -eu\n[ \"$1\" = apply-crl ]\ncat \"$3\" > \"$4\"\nprintf '%s' \"$0\" > \"$4.tool-path\"\n")
			digest := environmentPreflightBytesSHA256(program)
			if tampered {
				program = append(program, []byte("# changed after image verification\n")...)
			}
			input := base64.StdEncoding.EncodeToString(program) + "\n" + base64.StdEncoding.EncodeToString([]byte(`{"issuer":"original"}`)) + "\n" + base64.StdEncoding.EncodeToString([]byte(`{"signed":"original"}`)) + "\n"
			cmd := exec.CommandContext(context.Background(), "/bin/sh", "-ec", maintenanceInstallScript, "crl-maintenance", state, digest)
			cmd.Stdin = strings.NewReader(input)
			err := cmd.Run()
			if tampered {
				if err == nil {
					t.Fatal("tampered executable ran")
				}
				if _, err := os.Stat(state); !os.IsNotExist(err) {
					t.Fatal("tampered executable modified state")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(state)
			if string(raw) != `{"signed":"original"}` {
				t.Fatal("transport changed public signed bytes")
			}
			tool, _ := os.ReadFile(state + ".tool-path")
			if _, err := os.Stat(filepath.Dir(string(tool))); !os.IsNotExist(err) {
				t.Fatal("transient binary directory retained")
			}
		})
	}
}

func maintenanceTestStaticELF() []byte {
	raw := make([]byte, 64)
	copy(raw, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(raw[16:], 2)
	binary.LittleEndian.PutUint16(raw[18:], 62)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	return raw
}

func TestCRLMaintenanceInstallerExtractsImmutableImageAndRejectsCacheTamper(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, _ := newSecretStore(root, "staging")
	bin := t.TempDir()
	source := filepath.Join(bin, "source")
	calls := filepath.Join(bin, "calls")
	original := maintenanceTestStaticELF()
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CRL_BINARY_SOURCE", source)
	t.Setenv("CRL_BINARY_CALLS", calls)
	script := `#!/usr/bin/env python3
import os,sys,shutil
args=sys.argv[1:]
with open(os.environ['CRL_BINARY_CALLS'],'a') as f:f.write(args[0]+'\n')
if args[0]=='create':
 assert '--platform' in args and 'linux/amd64' in args
 assert '@sha256:' in args[-1]
 print('a'*64)
elif args[0]=='cp':
 assert args[1]=='a'*64+':/app/pkitrust'
 shutil.copyfile(os.environ['CRL_BINARY_SOURCE'],args[2])
elif args[0]!='rm':sys.exit(42)
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	image := "ghcr.io/test/api@sha256:" + strings.Repeat("b", 64)
	r := maintenanceRuntime{store: store, ensureImage: func(context.Context) (string, error) { return image, nil }}
	got, err := r.nativeTrustBinary(context.Background())
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("extraction: %v", err)
	}
	r.trustBinary = nil
	got, err = r.nativeTrustBinary(context.Background())
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("retained binary: %v", err)
	}
	events, _ := os.ReadFile(calls)
	if string(events) != "create\ncp\nrm\n" {
		t.Fatalf("cache recreated image container: %s", events)
	}
	path := filepath.Join(store.Root, crlMaintenanceBase, "helpers", environmentPreflightBytesSHA256([]byte(image)), "pkitrust")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("binary cache is not private")
	}
	if err := os.WriteFile(path, append(original, 1), 0600); err != nil {
		t.Fatal(err)
	}
	r.trustBinary = nil
	if _, err := r.nativeTrustBinary(context.Background()); err == nil {
		t.Fatal("tampered cache accepted")
	}
	eventsAfter, _ := os.ReadFile(calls)
	if !bytes.Equal(events, eventsAfter) {
		t.Fatal("tampered cache silently replaced")
	}
}

func TestCRLMaintenanceInstallerRejectsWrongArchitectureAndDynamicLoader(t *testing.T) {
	for _, mode := range []string{"invalid", "foreign-architecture", "dynamic-loader"} {
		raw := maintenanceTestStaticELF()
		switch mode {
		case "invalid":
			raw = []byte("not ELF")
		case "foreign-architecture":
			binary.LittleEndian.PutUint16(raw[18:], 183)
		case "dynamic-loader":
			binary.LittleEndian.PutUint64(raw[32:], 64)
			binary.LittleEndian.PutUint16(raw[54:], 56)
			binary.LittleEndian.PutUint16(raw[56:], 1)
			raw = append(raw, make([]byte, 56)...)
			binary.LittleEndian.PutUint32(raw[64:], 3)
		}
		if err := validateMaintenanceTrustBinary(raw); err == nil {
			t.Fatalf("unportable installer accepted: %s", mode)
		}
	}
}

func TestCRLMaintenanceInstallerUnavailableBlocksPublication(t *testing.T) {
	called := false
	r := maintenanceRuntime{lease: maintenanceTestLease{}, ensureImage: func(context.Context) (string, error) { called = true; return "", os.ErrNotExist }}
	if err := r.publish(context.Background(), crlMaintenanceIssuer{}, createRootCRLRequest{}, createRootCRLRecord{}); err == nil || !called || r.helper != nil {
		t.Fatal("publication began before native installer qualification")
	}
}
