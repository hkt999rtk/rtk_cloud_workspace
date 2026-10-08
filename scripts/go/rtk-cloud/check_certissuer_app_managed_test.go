package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type certIssuerManagedFixture struct {
	root, workspace, helper, state, ca, socket, inspectLog, opensslLog, pidFile string
	key, chain, leaf, authorities, rootPin                                      string
}

func newCertIssuerManagedFixture(t *testing.T) certIssuerManagedFixture {
	t.Helper()
	for _, tool := range []string{"perl", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("required fixture tool %s: %v", tool, err)
		}
	}
	f := certIssuerManagedFixture{root: t.TempDir(), rootPin: strings.Repeat("a", 64), socket: "/run/account-pki/private/controller.sock"}
	f.workspace = filepath.Join(f.root, "workspace")
	f.helper = filepath.Join(f.workspace, "scripts", "check-certissuer-app-managed.pl")
	f.state = filepath.Join(f.root, "owner", "identity.json")
	f.ca = filepath.Join(f.root, "issuer-ca.pem")
	f.inspectLog, f.opensslLog, f.pidFile = filepath.Join(f.root, "inspect.log"), filepath.Join(f.root, "openssl.log"), filepath.Join(f.root, "openssl.pid")
	pem := func(kind, content string) string {
		return "-----BEGIN " + kind + "-----\n" + base64.StdEncoding.EncodeToString([]byte(content)) + "\n-----END " + kind + "-----\n"
	}
	f.key = pem("PRIVATE KEY", "private-key-fixture-sentinel")
	f.leaf = pem("CERTIFICATE", "owner-leaf-fixture")
	f.authorities = pem("CERTIFICATE", "owner-intermediate-fixture") + pem("CERTIFICATE", "owner-root-fixture")
	f.chain = f.leaf + f.authorities
	for _, path := range []string{filepath.Dir(f.helper), filepath.Dir(f.state), filepath.Join(f.root, "bin"), filepath.Join(f.root, "base-only", "JSON"), filepath.Join(f.root, "base-only", "Digest")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.write(t, f.state, f.stateJSON(t), 0600)
	f.write(t, f.ca, f.authorities, 0600)
	// Only the remote/helper process sees these modules: the local main script
	// legitimately requires JSON::PP, while the deployed owner has perl-base.
	for _, module := range []string{"JSON/PP.pm", "Digest/SHA.pm"} {
		f.write(t, filepath.Join(f.root, "base-only", filepath.FromSlash(module)), "die 'fixture module is unavailable';\n", 0600)
	}
	native := filepath.Join(f.root, "bin", "native-inspect")
	f.write(t, native, `#!/bin/sh
set -eu
[ "$#" = 4 ] && [ "$1" = inspect ] && [ "$2" = "$PKI_MANAGEMENT_IDENTITY_STATE" ] && [ "$3" = service:account-manager ] && [ "$4" = "$PKI_MANAGEMENT_IDENTITY_ROOT_SHA256" ] || exit 2
printf 'inspect\n' >> "$RTK_TEST_INSPECT_LOG"
if [ "${RTK_TEST_NATIVE_ERROR:-}" = 1 ]; then printf 'private-native-error-sentinel\n' >&2; exit 1; fi
digest="$(sha256sum "$2")"; digest="${digest%% *}"
if [ "${RTK_TEST_DIGEST_MISMATCH:-}" = 1 ]; then digest=dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd; fi
pending=false
if [ "${RTK_TEST_NATIVE_PENDING:-}" = 1 ]; then pending=true; fi
if [ "${RTK_TEST_NATIVE_UNKNOWN:-}" = 1 ]; then printf '{"subject":"service:account-manager","fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","public_key_sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","root_sha256":"%s","state_sha256":"%s","pending":false,"unexpected":true}\n' "$4" "$digest"; exit 0; fi
printf '{"subject":"service:account-manager","fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","public_key_sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","root_sha256":"%s","state_sha256":"%s","pending":%s}\n' "$4" "$digest" "$pending"
`, 0700)
	for _, script := range []string{"check-certissuer-app-managed.pl", "check-certissuer-app-mtls.sh", "check-certissuer-app-helper.pl", "check-certissuer-app-socket.pl"} {
		raw, err := os.ReadFile(filepath.Join(mustWorkspaceRoot(t), "scripts", script))
		if err != nil {
			t.Fatal(err)
		}
		if script == "check-certissuer-app-managed.pl" {
			if !bytes.Contains(raw, []byte("/app/serviceidentity-bootstrap")) {
				t.Fatal("managed helper no longer uses the original owner's native inspector")
			}
			raw = bytes.ReplaceAll(raw, []byte("/app/serviceidentity-bootstrap"), []byte(native))
		}
		f.write(t, filepath.Join(f.workspace, "scripts", script), string(raw), 0700)
	}
	// The fake transport opens the actual inherited pipe descriptors. Credential
	// values live in this synthetic fixture, never in the probe argv or env.
	openssl := fmt.Sprintf(`#!/usr/bin/perl
use strict;
use warnings;
my $key = q{%s};
my $leaf = q{%s};
my $authorities = q{%s};
my $chain = $leaf . $authorities;
sub bad { die "fixture OpenSSL arguments or streams invalid\n" }
for my $value (@ARGV, values %%ENV) {
  bad() if index($value, $key) >= 0 || index($value, 'cHJpdmF0ZS1rZXktZml4dHVyZS1zZW50aW5lbA==') >= 0;
}
my %%option;
for (my $i = 0; $i < @ARGV; $i++) {
  my $arg = $ARGV[$i];
  if ($arg =~ /\A-(?:connect|servername|verify_hostname|cert|cert_chain|key|CAfile)\z/) { $option{$arg} = $ARGV[++$i] // bad(); }
}
bad() unless $ARGV[0] eq 's_client' && grep { $_ eq '-verify_return_error' } @ARGV;
bad() unless $option{'-connect'} eq $ENV{RTK_TEST_PUBLIC_HOST}.':443' && $option{'-servername'} eq $ENV{RTK_TEST_PUBLIC_HOST} && $option{'-verify_hostname'} eq $ENV{RTK_TEST_PUBLIC_HOST} && $option{'-CAfile'} eq $ENV{PKI_MANAGEMENT_ISSUER_TLS_CA};
open my $pid, '>', $ENV{RTK_TEST_OPENSSL_PID} or bad(); print {$pid} $$; close $pid;
sleep 1 if ($ENV{RTK_TEST_OPENSSL_MODE} // '') eq 'pause';
if (($ENV{RTK_TEST_OPENSSL_MODE} // '') eq 'hang') { sleep 30; exit 1; }
my $authenticated = exists $option{'-key'};
if ($authenticated) {
  my %%seen;
  my %%material;
  for my $kind ('-cert', '-key', '-cert_chain') {
    my $path = $option{$kind} // bad();
    if (($ENV{RTK_TEST_ALLOW_APP_TLS} // '') eq '1') {
      my $expected = $kind eq '-key' ? $ENV{APP_CERT_ISSUER_CLIENT_KEY} : $ENV{APP_CERT_ISSUER_CLIENT_CERT};
      bad() unless $path eq $expected;
    } else { bad() unless $path =~ m{\A/(?:dev/fd|proc/self/fd)/[0-9]+\z} && !$seen{$path}++; }
    open my $pipe, '<', $path or bad();
    local $/; $material{$kind} = <$pipe> // ''; close $pipe;
  }
  bad() unless $material{'-key'} eq $key && ($material{'-cert'} eq $leaf || $material{'-cert'} eq $chain) && ($material{'-cert_chain'} eq $authorities || $material{'-cert_chain'} eq $chain);
} else { bad() if exists $option{'-cert'} || exists $option{'-cert_chain'}; }
my $request = do { local $/; <STDIN> // '' };
my $host = $ENV{RTK_TEST_PUBLIC_HOST};
bad() unless $request eq "POST /v1/certificates/app/issue HTTP/1.1\r\nHost: $host\r\nContent-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}";
open my $log, '>>', $ENV{RTK_TEST_OPENSSL_LOG} or bad(); print {$log} ($authenticated ? 'authenticated' : 'anonymous'), "\n"; close $log;
if (($ENV{RTK_TEST_OPENSSL_MODE} // '') eq 'oversized-stdout') { $| = 1; print 'x' x 65537; sleep 30; exit 1; }
if (($ENV{RTK_TEST_OPENSSL_MODE} // '') eq 'oversized-stderr') { print STDERR 'x' x 65537; sleep 30; exit 1; }
if (($ENV{RTK_TEST_OPENSSL_MODE} // '') eq 'private-error') { print STDERR $key, "private-openssl-error-sentinel\n"; exit 1; }
if (($ENV{RTK_TEST_OPENSSL_MODE} // '') eq 'invalid-response') { print "private-openssl-stdout-sentinel\n"; exit 0; }
if (($ENV{RTK_TEST_OPENSSL_MODE} // '') eq 'verify-error') { print STDERR "verify error:num=62 hostname mismatch\n"; exit 1; }
if (($ENV{RTK_TEST_OPENSSL_MODE} // '') eq 'transport-failed') { print STDERR "connect:errno=111\n"; exit 1; }
if (($ENV{RTK_TEST_OPENSSL_MODE} // '') eq 'rotate') { open my $state, '>>', $ENV{PKI_MANAGEMENT_IDENTITY_STATE} or bad(); print {$state} ' '; close $state; }
if (!$authenticated && ($ENV{RTK_TEST_ANONYMOUS_ACCEPTED} // '') ne '1') { print STDERR "tlsv13 alert certificate required\n"; exit 1; }
my $body = '{"error":{"code":"user_id_required"}}';
print "HTTP/1.1 400 Bad Request\r\nContent-Length: ", length($body), "\r\nConnection: close\r\n\r\n", $body;
`, f.key, f.leaf, f.authorities)
	f.write(t, filepath.Join(f.root, "bin", "openssl"), openssl, 0700)
	return f
}

func (f certIssuerManagedFixture) write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func (f certIssuerManagedFixture) stateJSON(t *testing.T) string {
	t.Helper()
	state := struct {
		Version int    `json:"version"`
		Subject string `json:"subject"`
		Current struct {
			PrivateKeyPEM    string `json:"private_key_pem"`
			CertificateChain string `json:"certificate_chain_pem"`
			InstalledAt      string `json:"installed_at"`
		} `json:"current"`
	}{Version: 1, Subject: "service:account-manager"}
	state.Current.PrivateKeyPEM, state.Current.CertificateChain, state.Current.InstalledAt = f.key, f.chain, "2026-10-08T00:00:00Z"
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (f certIssuerManagedFixture) env(overrides map[string]string, baseOnly bool) []string {
	values := make(map[string]string)
	for _, value := range os.Environ() {
		key, val, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(key, "PKI_") && !strings.HasPrefix(key, "APP_CERT_ISSUER_") {
			values[key] = val
		}
	}
	for key, val := range map[string]string{
		"PATH": filepath.Join(f.root, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME": f.root, "PKI_ENVIRONMENT": "staging", "PKI_MANAGEMENT_IDENTITY_STATE": f.state,
		"PKI_MANAGEMENT_IDENTITY_ROOT_SHA256": f.rootPin, "PKI_MANAGEMENT_SOCKET": f.socket,
		"PKI_MANAGEMENT_ISSUER_TLS_CA": f.ca, "APP_CERT_ISSUER_SOCKET": f.socket,
		"RTK_TEST_PUBLIC_HOST": "issuer.example.test", "RTK_TEST_INSPECT_LOG": f.inspectLog,
		"RTK_TEST_OPENSSL_LOG": f.opensslLog, "RTK_TEST_OPENSSL_PID": f.pidFile,
		"RTK_TEST_BASE_ONLY":    filepath.Join(f.root, "base-only"),
		"RTK_CLOUD_CONFIG_ROOT": filepath.Join(f.root, "config"), "RTK_CLOUD_KUBECONFIG": "",
		"RTK_CLOUD_LKE_KUBECONFIG": "", "KUBECONFIG": "",
	} {
		values[key] = val
	}
	if baseOnly {
		values["PERL5LIB"] = filepath.Join(f.root, "base-only")
	}
	for key, val := range overrides {
		values[key] = val
	}
	result := make([]string, 0, len(values))
	for key, val := range values {
		result = append(result, key+"="+val)
	}
	return result
}

func (f certIssuerManagedFixture) run(t *testing.T, overrides map[string]string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "perl", f.helper, "issuer.example.test", f.socket, "staging")
	cmd.Env = f.env(overrides, true)
	return cmd.CombinedOutput()
}

func (f certIssuerManagedFixture) assertPrivateOutput(t *testing.T, out []byte) {
	t.Helper()
	for _, sentinel := range []string{f.key, "cHJpdmF0ZS1rZXktZml4dHVyZS1zZW50aW5lbA==", "private-native-error-sentinel", "private-openssl-error-sentinel", "private-openssl-stdout-sentinel"} {
		if bytes.Contains(out, []byte(sentinel)) {
			t.Fatalf("private diagnostic or credential escaped the owner probe: %q", out)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(f.state))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(f.state) {
		t.Fatalf("probe created owner state, locks or credential exports: entries=%v err=%v", entries, err)
	}
}

func TestCertIssuerManagedProbeUsesPrivatePipesAndNativeSnapshot(t *testing.T) {
	f := newCertIssuerManagedFixture(t)
	before := f.stateJSON(t)
	out, err := f.run(t, nil)
	if err != nil || !bytes.HasPrefix(out, []byte("HTTP/1.1 400")) || !bytes.Contains(out, []byte(`"code":"user_id_required"`)) {
		t.Fatalf("private owner probe failed: %v %s", err, out)
	}
	f.assertPrivateOutput(t, out)
	state, _ := os.ReadFile(f.state)
	inspections, _ := os.ReadFile(f.inspectLog)
	transport, _ := os.ReadFile(f.opensslLog)
	if string(state) != before || string(inspections) != "inspect\ninspect\n" || string(transport) != "authenticated\n" {
		t.Fatalf("probe mutated state or omitted snapshot fences: inspect=%q transport=%q", inspections, transport)
	}
}

func TestCertIssuerManagedProbeRejectsInvalidStateAndRedactsFailures(t *testing.T) {
	for _, name := range []string{"malformed", "unknown-field", "duplicate-field", "pending", "domain", "dns-names", "version", "subject", "private-mode", "symlink", "oversized-state", "wrong-environment", "wrong-socket", "digest-mismatch", "native-pending", "native-unknown", "native-error", "rotation", "private-error", "invalid-response", "oversized-stdout", "oversized-stderr"} {
		t.Run(name, func(t *testing.T) {
			f := newCertIssuerManagedFixture(t)
			env := map[string]string{}
			code := "CERTISSUER_MANAGED_IDENTITY_INVALID"
			state := f.stateJSON(t)
			switch name {
			case "malformed":
				state = "{broken"
			case "unknown-field":
				state = strings.TrimSuffix(state, "}") + `,"unexpected":"private-native-error-sentinel"}`
			case "duplicate-field":
				state = strings.Replace(state, `"version":1`, `"version":1,"version":1`, 1)
			case "pending":
				state = strings.TrimSuffix(state, "}") + `,"pending":{"private_key_pem":"private-native-error-sentinel"}}`
			case "domain":
				state = strings.Replace(state, `"current":`, `"domain":"service","current":`, 1)
			case "dns-names":
				state = strings.Replace(state, `"current":`, `"dns_names":[],"current":`, 1)
			case "version":
				state = strings.Replace(state, `"version":1`, `"version":2`, 1)
			case "subject":
				state = strings.Replace(state, "service:account-manager", "service:foreign", 1)
			case "oversized-state":
				state += strings.Repeat(" ", 262144)
			case "private-mode":
				if err := os.Chmod(f.state, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(f.root, "other-state.json")
				f.write(t, target, state, 0600)
				if err := os.Remove(f.state); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, f.state); err != nil {
					t.Fatal(err)
				}
			case "wrong-environment":
				env["PKI_ENVIRONMENT"], code = "dev", "CERTISSUER_CONFIG_INVALID"
			case "wrong-socket":
				env["PKI_MANAGEMENT_SOCKET"], code = "/run/foreign.sock", "CERTISSUER_CONFIG_INVALID"
			case "digest-mismatch":
				env["RTK_TEST_DIGEST_MISMATCH"], code = "1", "CERTISSUER_MANAGED_STATE_CHANGED"
			case "native-pending":
				env["RTK_TEST_NATIVE_PENDING"] = "1"
			case "native-unknown":
				env["RTK_TEST_NATIVE_UNKNOWN"] = "1"
			case "native-error":
				env["RTK_TEST_NATIVE_ERROR"] = "1"
			case "rotation":
				env["RTK_TEST_OPENSSL_MODE"], code = "rotate", "CERTISSUER_MANAGED_STATE_CHANGED"
			case "private-error":
				env["RTK_TEST_OPENSSL_MODE"], code = "private-error", "CERTISSUER_MANAGED_TRANSPORT_FAILED"
			case "invalid-response":
				env["RTK_TEST_OPENSSL_MODE"], code = "invalid-response", "CERTISSUER_MANAGED_RESPONSE_INVALID"
			case "oversized-stdout", "oversized-stderr":
				env["RTK_TEST_OPENSSL_MODE"], code = name, "CERTISSUER_MANAGED_RESPONSE_INVALID"
			}
			if name != "private-mode" && name != "symlink" {
				f.write(t, f.state, state, 0600)
			}
			out, err := f.run(t, env)
			if err == nil || !bytes.Contains(out, []byte(code)) {
				t.Fatalf("expected %s: err=%v output=%s", code, err, out)
			}
			f.assertPrivateOutput(t, out)
			if bytes.HasPrefix(out, []byte("HTTP/")) {
				t.Fatal("failed owner qualification returned transport success")
			}
			if name == "oversized-stdout" || name == "oversized-stderr" {
				raw, _ := os.ReadFile(f.pidFile)
				child, _ := strconv.Atoi(string(raw))
				if child <= 0 {
					t.Fatal("oversized transport did not start")
				}
				if err := syscall.Kill(child, 0); err != syscall.ESRCH {
					_ = syscall.Kill(child, syscall.SIGKILL)
					t.Fatalf("oversized transport was not reaped: pid=%d err=%v", child, err)
				}
			}
		})
	}
}

func TestCertIssuerManagedAnonymousProbeRequiresExplicitVerifiedDenial(t *testing.T) {
	for _, name := range []string{"denied", "accepted", "verification-failure", "connection-failure", "private-error"} {
		t.Run(name, func(t *testing.T) {
			f := newCertIssuerManagedFixture(t)
			// Anonymous qualification uses trust only. An unusable private state
			// and failed native inspector must not cause a credential read.
			f.write(t, f.state, "unusable-private-state-fixture", 0600)
			env := map[string]string{"RTK_TEST_NATIVE_ERROR": "1"}
			switch name {
			case "accepted":
				env["RTK_TEST_ANONYMOUS_ACCEPTED"] = "1"
			case "verification-failure":
				env["RTK_TEST_OPENSSL_MODE"] = "verify-error"
			case "connection-failure":
				env["RTK_TEST_OPENSSL_MODE"] = "transport-failed"
			case "private-error":
				env["RTK_TEST_OPENSSL_MODE"] = "private-error"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "perl", f.helper, "issuer.example.test", f.socket, "staging", "anonymous")
			cmd.Env = f.env(env, true)
			out, err := cmd.CombinedOutput()
			if name == "denied" {
				if err != nil || string(out) != "tlsv13 alert certificate required\nRTK_ANONYMOUS_EXIT=1\n" {
					t.Fatalf("verified anonymous denial failed: %v %s", err, out)
				}
			} else if err == nil || !bytes.Contains(out, []byte("CERTISSUER_ANONYMOUS_DENIAL_UNVERIFIED")) {
				t.Fatalf("anonymous %s incorrectly proved denial: %v %s", name, err, out)
			}
			if _, err := os.Stat(f.inspectLog); !os.IsNotExist(err) {
				t.Fatal("anonymous probe inspected private identity state")
			}
			f.assertPrivateOutput(t, out)
		})
	}
}

func (f certIssuerManagedFixture) assertScratchClean(t *testing.T, environment string) {
	t.Helper()
	path := filepath.Join(f.root, "config")
	for _, part := range []string{"", environment, "deployment", "tmp"} {
		if part != "" {
			path = filepath.Join(path, part)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatalf("canonical scratch parent is not a private real directory: %s %v", path, err)
		}
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe left scratch material: entries=%v err=%v", entries, err)
	}
}

func TestCertIssuerPublicProbeScratchUsesPrivateSelectedEnvironment(t *testing.T) {
	f := newCertIssuerManagedFixture(t)
	f.prepareMain(t, "staging")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(f.workspace, "scripts", "check-certissuer-app-mtls.sh"), "staging", "--public-host", "issuer.example.test")
	cmd.Env = f.env(map[string]string{"RTK_TEST_OPENSSL_MODE": "pause", "TMPDIR": filepath.Join(f.root, "forbidden-os-temp")}, false)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(f.pidFile); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	root := filepath.Join(f.root, "config", "staging", "deployment", "tmp")
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "certissuer-public-") {
		cancel()
		<-done
		t.Fatalf("active probe did not use canonical selected scratch: entries=%v err=%v", entries, err)
	}
	err = filepath.Walk(filepath.Join(root, entries[0].Name()), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		want := os.FileMode(0600)
		if info.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("scratch %s has unsafe permissions %v", path, info.Mode())
		}
		return nil
	})
	if err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("private scratch probe failed: %v %s", err, out.String())
	}
	f.assertScratchClean(t, "staging")
	f.assertPrivateOutput(t, out.Bytes())
	if _, err := os.Stat(filepath.Join(f.root, "forbidden-os-temp")); !os.IsNotExist(err) {
		t.Fatal("probe fell back to the operating system temporary directory")
	}
}

func TestCertIssuerPublicProbeRejectsUnsafeScratchComponents(t *testing.T) {
	for _, component := range []string{"root", "environment", "deployment", "tmp"} {
		for _, unsafe := range []string{"permissions", "symlink"} {
			t.Run(component+"/"+unsafe, func(t *testing.T) {
				f := newCertIssuerManagedFixture(t)
				f.prepareMain(t, "staging")
				root := filepath.Join(f.root, "config")
				path := root
				for _, part := range []string{"staging", "deployment", "tmp"} {
					path = filepath.Join(path, part)
					if err := os.MkdirAll(path, 0700); err != nil {
						t.Fatal(err)
					}
				}
				path = map[string]string{"root": root, "environment": filepath.Join(root, "staging"), "deployment": filepath.Join(root, "staging", "deployment"), "tmp": path}[component]
				if unsafe == "permissions" {
					if err := os.Chmod(path, 0755); err != nil {
						t.Fatal(err)
					}
				} else {
					target := filepath.Join(f.root, "outside-selected-store")
					if err := os.Rename(path, target); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "bash", filepath.Join(f.workspace, "scripts", "check-certissuer-app-mtls.sh"), "staging", "--public-host", "issuer.example.test")
				cmd.Env = f.env(map[string]string{"TMPDIR": filepath.Join(f.root, "forbidden-os-temp")}, false)
				out, err := cmd.CombinedOutput()
				if err == nil || !bytes.Contains(out, []byte("FAIL [TEMP_PATH_INVALID]")) {
					t.Fatalf("unsafe selected scratch accepted: %v %s", err, out)
				}
				f.assertPrivateOutput(t, out)
				if _, err := os.Stat(f.inspectLog); !os.IsNotExist(err) {
					t.Fatal("unsafe scratch path allowed owner credential inspection")
				}
				if _, err := os.Stat(filepath.Join(f.root, "forbidden-os-temp")); !os.IsNotExist(err) {
					t.Fatal("unsafe scratch path fell back to operating system storage")
				}
			})
		}
	}
}

func TestCertIssuerManagedProbeCancellationAndTimeoutReapTransport(t *testing.T) {
	for _, trigger := range []string{"cancellation", "timeout"} {
		t.Run(trigger, func(t *testing.T) {
			f := newCertIssuerManagedFixture(t)
			if trigger == "timeout" {
				raw, err := os.ReadFile(f.helper)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Count(raw, []byte("alarm 12;")) != 1 {
					t.Fatal("probe deadline fixture no longer matches its production alarm")
				}
				f.write(t, f.helper, string(bytes.Replace(raw, []byte("alarm 12;"), []byte("alarm 1;"), 1)), 0700)
			}
			cmd := exec.Command("perl", f.helper, "issuer.example.test", f.socket, "staging")
			cmd.Env = f.env(map[string]string{"RTK_TEST_OPENSSL_MODE": "hang"}, true)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			deadline := time.Now().Add(3 * time.Second)
			var child int
			for time.Now().Before(deadline) {
				raw, err := os.ReadFile(f.pidFile)
				if err == nil {
					child, _ = strconv.Atoi(string(raw))
					if child > 0 {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if child == 0 {
				_ = cmd.Process.Kill()
				<-done
				t.Fatalf("transport did not start: %s", out.String())
			}
			t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
			if trigger == "cancellation" {
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled probe reported success")
				}
			case <-time.After(3 * time.Second):
				_ = cmd.Process.Kill()
				_ = syscall.Kill(child, syscall.SIGKILL)
				<-done
				t.Fatal("cancelled probe did not close and reap its transport")
			}
			if err := syscall.Kill(child, 0); err != syscall.ESRCH {
				t.Fatalf("transport survived cancellation: pid=%d err=%v", child, err)
			}
			f.assertPrivateOutput(t, out.Bytes())
			if trigger == "timeout" && !bytes.Contains(out.Bytes(), []byte("CERTISSUER_MANAGED_PROBE_TIMEOUT")) {
				t.Fatalf("deadline did not classify the timed-out probe: %s", out.String())
			}
		})
	}
}

func TestCertIssuerPublicManagedProbeSelectsOriginalOwnerInEveryEnvironment(t *testing.T) {
	for _, tc := range []struct{ environment, mode string }{{"dev", "managed"}, {"staging", "managed"}, {"production", "managed"}, {"staging", "anonymous-accepted"}, {"staging", "legacy"}, {"staging", "socket-only"}} {
		t.Run(tc.environment+"/"+tc.mode, func(t *testing.T) {
			environment := tc.environment
			f := newCertIssuerManagedFixture(t)
			env := map[string]string{"PKI_ENVIRONMENT": environment}
			var socketResult chan error
			if tc.mode == "anonymous-accepted" {
				env["RTK_TEST_ANONYMOUS_ACCEPTED"] = "1"
			}
			if tc.mode == "legacy" {
				cert, key := filepath.Join(f.root, "existing-client.pem"), filepath.Join(f.root, "existing-client.key")
				f.write(t, cert, f.chain, 0600)
				f.write(t, key, f.key, 0600)
				env["RTK_TEST_ALLOW_APP_TLS"], env["APP_CERT_ISSUER_SOCKET"] = "1", ""
				env["APP_CERT_ISSUER_CLIENT_CERT"], env["APP_CERT_ISSUER_CLIENT_KEY"], env["APP_CERT_ISSUER_CA_FILE"] = cert, key, f.ca
			}
			if tc.mode == "socket-only" {
				// macOS Unix sockets have a short pathname limit; Go's descriptive
				// test directory may exceed it before the socket name is appended.
				socketRoot, err := os.MkdirTemp("/tmp", "rtk-issuer-socket-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
				f.socket = filepath.Join(socketRoot, "original.sock")
				env["RTK_TEST_APP_SOCKET_ONLY"], env["APP_CERT_ISSUER_BASE_URL"] = "1", "https://issuer.internal.svc:9443"
				listener, err := net.Listen("unix", f.socket)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = listener.Close() })
				socketResult = make(chan error, 1)
				go func() {
					connection, err := listener.Accept()
					if err != nil {
						socketResult <- err
						return
					}
					defer connection.Close()
					_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
					want := "POST /v1/certificates/app/issue HTTP/1.1\r\nHost: issuer.internal.svc:9443\r\nContent-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}"
					request := make([]byte, len(want))
					_, err = io.ReadFull(connection, request)
					if err != nil || string(request) != want {
						socketResult <- fmt.Errorf("original socket request mismatch: %v", err)
						return
					}
					_, err = io.WriteString(connection, "HTTP/1.1 400 Bad Request\r\nContent-Length: 37\r\nConnection: close\r\n\r\n{\"error\":{\"code\":\"user_id_required\"}}")
					socketResult <- err
				}()
			}
			f.prepareMain(t, environment)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			args := []string{filepath.Join(f.workspace, "scripts", "check-certissuer-app-mtls.sh"), environment}
			if tc.mode != "socket-only" {
				args = append(args, "--public-host", "issuer.example.test")
			}
			cmd := exec.CommandContext(ctx, "bash", args...)
			cmd.Env = f.env(env, false)
			out, err := cmd.CombinedOutput()
			f.assertScratchClean(t, environment)
			if tc.mode == "anonymous-accepted" {
				if err == nil || !bytes.Contains(out, []byte("ANONYMOUS_DENIAL_UNVERIFIED")) || bytes.Contains(out, []byte("PASS: public CertIssuer mTLS validation and anonymous TLS denial verified")) {
					t.Fatalf("accepted anonymous TLS incorrectly qualified public route: %v %s", err, out)
				}
				f.assertPrivateOutput(t, out)
				return
			}
			if tc.mode == "socket-only" {
				if err != nil || !bytes.Contains(out, []byte("PASS: managed CertIssuer socket request reached authenticated request validation")) {
					t.Fatalf("original socket probe failed: %v %s", err, out)
				}
				if socketErr := <-socketResult; socketErr != nil {
					t.Fatal(socketErr)
				}
				f.assertPrivateOutput(t, out)
				if _, err := os.Stat(f.inspectLog); !os.IsNotExist(err) {
					t.Fatal("internal socket check read the private identity")
				}
				if _, err := os.Stat(f.opensslLog); !os.IsNotExist(err) {
					t.Fatal("internal socket check substituted direct public TLS")
				}
				return
			}
			if err != nil || !bytes.Contains(out, []byte("PASS: public CertIssuer mTLS validation and anonymous TLS denial verified")) {
				t.Fatalf("public owner probe failed: %v %s", err, out)
			}
			f.assertPrivateOutput(t, out)
			transport, _ := os.ReadFile(f.opensslLog)
			if string(transport) != "authenticated\nanonymous\n" {
				t.Fatalf("public qualification did not prove both calls: %q", transport)
			}
			if tc.mode == "legacy" {
				if _, err := os.Stat(f.inspectLog); !os.IsNotExist(err) {
					t.Fatal("legacy app TLS substituted the managed owner")
				}
			}
		})
	}
}

func (f certIssuerManagedFixture) prepareMain(t *testing.T, environment string) {
	t.Helper()
	for _, dir := range []string{filepath.Join(f.workspace, "cloud_env", environment), filepath.Join(f.root, "config", environment, "kube")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.write(t, filepath.Join(f.workspace, "cloud_env", environment, "environment.env"), "CLOUD_STACK_NAME=video-cloud-"+environment+"\n", 0600)
	f.write(t, filepath.Join(f.workspace, "cloud_env", environment, "deployment.env"), "\n", 0600)
	f.write(t, filepath.Join(f.root, "config", environment, "kube", "kubeconfig.yaml"), "isolated-fixture-kubeconfig\n", 0600)
	f.write(t, filepath.Join(f.root, "bin", "kubectl"), `#!/usr/bin/perl
use strict;
use warnings;
my @args = @ARGV;
my $kube = $ENV{RTK_CLOUD_CONFIG_ROOT}.'/'.$ENV{PKI_ENVIRONMENT}.'/kube/kubeconfig.yaml';
die "fixture selected foreign environment\n" unless $args[0] eq '--kubeconfig' && $args[1] eq $kube;
my $namespace = 'video-cloud-'.$ENV{PKI_ENVIRONMENT}.'-account-manager';
my $i = 2; $i++ while $i < @args && $args[$i] ne '-n';
die "fixture selected foreign namespace\n" unless ($args[$i+1] // '') eq $namespace;
$i += 2;
if (($args[$i] // '') eq 'get') {
  die "fixture unexpected resource\n" unless ($args[$i+1] // '') eq 'pods';
  print '{"items":[{"metadata":{"name":"account-manager-fixture"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}]}'; exit 0;
}
die "fixture unexpected mutation\n" unless ($args[$i++] // '') eq 'exec';
$i++ if ($args[$i] // '') eq '-i';
die "fixture selected foreign Pod\n" unless ($args[$i++] // '') eq 'account-manager-fixture';
die "fixture missing explicit container\n" unless ($args[$i++] // '') eq '-c';
my $container = $args[$i++];
die "fixture missing exec boundary\n" unless ($args[$i++] // '') eq '--';
my @command = @args[$i..$#args];
if ($container eq 'app') {
  if ($command[0] eq 'sh' && $command[2] =~ /APP_CERT_ISSUER_SOCKET/) { print $ENV{APP_CERT_ISSUER_SOCKET}; exit 0; }
  die "fixture exported identity or used app TLS\n" unless ($command[0] eq 'sh' && ($ENV{RTK_TEST_ALLOW_APP_TLS} // '') eq '1') || ($command[0] eq 'perl' && ($ENV{RTK_TEST_APP_SOCKET_ONLY} // '') eq '1');
  exec @command or die "fixture original app execution failed\n";
}
die "fixture selected another owner\n" unless $container eq 'pkimanagement';
$ENV{PERL5LIB} = $ENV{RTK_TEST_BASE_ONLY};
exec @command or die "fixture owner execution failed\n";
`, 0700)
}
