package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/crypto/acme"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestPublicTLSIdentityExecutionContractNeverReplacesIncompleteOriginalKeys(t *testing.T) {
	for _, mode := range []string{"missing-keys", "missing-keys-and-cache", "missing-keys-and-directory", "foreign-key-plan", "malformed-account-key", "malformed-leaf-key", "malformed-CSR", "foreign-CSR-names", "foreign-CSR-key", "invalid-account", "foreign-account-origin", "insecure-account", "account-fragment", "missing-order", "foreign-order-intent"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, execution, _, plan, ops, server, _, apply := publicTLSProtocolFixture(t)
			if err := executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, execution, plan, apply, ops); err != nil {
				t.Fatal("intact native issuance fixture failed", err)
			}
			base := publicTLSBase(plan)
			var keys environmentPublicTLSKeys
			var account acme.Account
			if publicTLSReadJSON(store, filepath.Join(base, "keys.json"), &keys) != nil || publicTLSReadJSON(store, filepath.Join(base, "account.json"), &account) != nil {
				t.Fatal("original durable key/account absent")
			}
			keyChanged, accountChanged := false, false
			switch mode {
			case "missing-keys":
				if err := os.Remove(filepath.Join(store.Root, base, "keys.json")); err != nil {
					t.Fatal(err)
				}
			case "missing-keys-and-cache", "missing-keys-and-directory":
				entries, err := os.ReadDir(filepath.Join(store.Root, base))
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.IsDir() {
						t.Fatal("unexpected nested original cache")
					}
					if err := os.Remove(filepath.Join(store.Root, base, entry.Name())); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "missing-keys-and-directory" {
					if err := os.Remove(filepath.Join(store.Root, base)); err != nil {
						t.Fatal(err)
					}
				}
			case "foreign-key-plan":
				keys.PlanSHA256 = deploymentplan.Digest("foreign")
				keyChanged = true
			case "malformed-account-key":
				keys.AccountKey = "not-the-original-account-key"
				keyChanged = true
			case "malformed-leaf-key":
				keys.LeafKey = "not-the-original-leaf-key"
				keyChanged = true
			case "malformed-CSR":
				keys.CSR = "invalid-base64-CSR"
				keyChanged = true
			case "foreign-CSR-names", "foreign-CSR-key":
				block, _ := pem.Decode([]byte(keys.LeafKey))
				if block == nil {
					t.Fatal("original leaf key absent")
				}
				parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
				if err != nil {
					t.Fatal(err)
				}
				alternateKey, ok := parsed.(*ecdsa.PrivateKey)
				if !ok {
					t.Fatal("original test key is not ECDSA")
				}
				if mode == "foreign-CSR-key" {
					alternateKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
					if err != nil {
						t.Fatal(err)
					}
				}
				names := plan.Hosts
				if mode == "foreign-CSR-names" {
					names = []string{"foreign.example.test"}
				}
				csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: names[0]}, DNSNames: names}, alternateKey)
				if err != nil {
					t.Fatal(err)
				}
				keys.CSR, keyChanged = base64.StdEncoding.EncodeToString(csr), true
			case "invalid-account":
				account.Status = acme.StatusDeactivated
				accountChanged = true
			case "foreign-account-origin":
				account.URI = "https://foreign.example.test/account/1"
				accountChanged = true
			case "insecure-account":
				account.URI = "http://foreign.example.test/account/1"
				accountChanged = true
			case "account-fragment":
				account.URI += "#foreign"
				accountChanged = true
			case "missing-order":
				if err := os.Remove(filepath.Join(store.Root, base, "order.json")); err != nil {
					t.Fatal(err)
				}
			case "foreign-order-intent":
				entry := createOpenBaoACKCompletionEntry(execution.Journal, "public-tls/certificate")
				entry.IntentSHA256 = deploymentplan.Digest("foreign order intent")
				if err := execution.save(); err != nil {
					t.Fatal(err)
				}
			}
			if keyChanged {
				if err := store.write(filepath.Join(base, "keys.json"), mustProviderRecoveryJSON(keys), true); err != nil {
					t.Fatal(err)
				}
			}
			if accountChanged {
				if err := store.write(filepath.Join(base, "account.json"), mustProviderRecoveryJSON(account), true); err != nil {
					t.Fatal(err)
				}
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			accounts, orders, finalizes := server.accounts, server.orders, server.finalizes
			var issueErr error
			if mode == "missing-keys-and-cache" || mode == "missing-keys-and-directory" {
				// No cached material can short-circuit this actual recovery entry.
				issueErr = executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, execution, plan, apply, ops)
			} else {
				_, issueErr = issueEnvironmentPublicTLS(t.Context(), cfg, store, execution, plan, ops)
			}
			if issueErr == nil {
				t.Fatal("incomplete or foreign original identity was accepted", mode)
			}
			if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, store.Root)) {
				t.Fatal("refused issuance regenerated missing original key/CSR or rewrote retained state", mode)
			}
			if accounts != server.accounts || orders != server.orders || finalizes != server.finalizes {
				t.Fatal("refused identity reached new native account/order/certificate mutation", mode)
			}
		})
	}
}
