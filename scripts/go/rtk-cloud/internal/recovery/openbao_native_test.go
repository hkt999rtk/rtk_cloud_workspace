package recovery

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

// Explicit opt-in: disposable local Docker resources only, no implicit image
// pull, no environment credentials, and no production qualification claim.
// RTK_OPENBAO_NATIVE_TEST_IMAGE must name an already available immutable image.
func TestNativeOpenBaoOriginalSealMigrationHAAndRestore(t *testing.T) {
	image := os.Getenv("RTK_OPENBAO_NATIVE_TEST_IMAGE")
	if image == "" {
		t.Skip("set RTK_OPENBAO_NATIVE_TEST_IMAGE to an available Docker image ID/digest for the isolated native drill")
	}
	if !strings.HasPrefix(image, "sha256:") && !strings.Contains(image, "@sha256:") {
		t.Fatal("native drill requires an immutable local image ID/digest")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	docker := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).Output()
		if err != nil {
			t.Fatalf("isolated Docker operation %s failed (details suppressed)", args[0])
		}
		return out
	}
	docker("image", "inspect", image)
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	prefix := "rtk-bao-drill-" + hex.EncodeToString(nonce)
	docker("network", "create", prefix)
	var containers, volumes []string
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		for _, name := range containers {
			_ = exec.CommandContext(cleanup, "docker", "rm", "-f", name).Run()
		}
		for _, name := range volumes {
			_ = exec.CommandContext(cleanup, "docker", "volume", "rm", name).Run()
		}
		_ = exec.CommandContext(cleanup, "docker", "network", "rm", prefix).Run()
	})
	volume := func(suffix string) string {
		name := prefix + "-" + suffix
		docker("volume", "create", name)
		volumes = append(volumes, name)
		return name
	}
	directory := t.TempDir()
	caPEM, certPEM, keyPEM, fingerprint := nativeBaoTLS(t)
	for name, raw := range map[string][]byte{"ca.crt": caPEM, "tls.crt": certPEM, "tls.key": keyPEM} {
		if err := os.WriteFile(filepath.Join(directory, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "openbao-internal", MinVersion: tls.VersionTLS12}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	token := ""
	request := func(address, method, path string, input any, target any) int {
		t.Helper()
		var body io.Reader
		if input != nil {
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, address+"/v1/"+path, body)
		if err != nil {
			t.Fatal("invalid native drill request")
		}
		req.Header.Set("X-Vault-Token", token)
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			return 0
		}
		defer response.Body.Close()
		if response.StatusCode >= 200 && response.StatusCode < 300 && target != nil {
			if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target) != nil {
				t.Fatal("invalid native drill response")
			}
		}
		return response.StatusCode
	}
	wait := func(label string, predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(75 * time.Second)
		for time.Now().Before(deadline) && ctx.Err() == nil {
			if predicate() {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatalf("native drill did not reach %s (details suppressed)", label)
	}
	type node struct{ name, address string }
	start := func(index int, backend, dataVolume string) node {
		name := fmt.Sprintf("%s-node-%d", prefix, index)
		host := fmt.Sprintf("node-%d", index)
		config := fmt.Sprintf(`ui = false
disable_mlock = true
api_addr = "https://%s:8200"
cluster_addr = "https://%s:8201"
listener "tcp" {
  address = "[::]:8200"
  cluster_address = "[::]:8201"
  tls_cert_file = "/fixture/tls.crt"
  tls_key_file = "/fixture/tls.key"
}
storage "%s" {
  path = "/openbao/data"
`, host, host, backend)
		if backend == "raft" {
			config += fmt.Sprintf("  node_id = %q\n", host)
			for peer := 0; peer < 3; peer++ {
				config += fmt.Sprintf("  retry_join {\n    leader_api_addr = \"https://node-%d:8200\"\n    leader_ca_cert_file = \"/fixture/ca.crt\"\n    leader_tls_servername = \"openbao-internal\"\n  }\n", peer)
			}
		}
		config += `}
audit "file" "deployment-audit" {
  options {
    file_path = "/openbao/audit/audit.jsonl"
    log_raw = "false"
  }
}
`
		file := fmt.Sprintf("node-%d.hcl", index)
		if err := os.WriteFile(filepath.Join(directory, file), []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		auditVolume := volume(fmt.Sprintf("audit-%d-%s", index, backend))
		containers = append(containers, name)
		docker("run", "--pull=never", "-d", "--name", name, "--network", prefix, "--network-alias", host, "--user", "0:0", "-p", "127.0.0.1::8200", "-v", directory+":/fixture:ro", "-v", dataVolume+":/openbao/data", "-v", auditVolume+":/openbao/audit", "--entrypoint", "bao", image, "server", "-config=/fixture/"+file)
		port := strings.TrimSpace(string(docker("port", name, "8200/tcp")))
		if !strings.HasPrefix(port, "127.0.0.1:") || strings.ContainsAny(port, "\r\n") {
			t.Fatal("native drill port is not isolated")
		}
		n := node{name, "https://" + port}
		wait("TLS seal-status", func() bool { return request(n.address, "GET", "sys/seal-status", nil, nil) == 200 })
		return n
	}
	fileVolume := volume("file-data")
	leader := start(0, "file", fileVolume)
	var initialized struct {
		Keys      []string `json:"keys_base64"`
		RootToken string   `json:"root_token"`
	}
	if request(leader.address, "PUT", "sys/init", map[string]int{"secret_shares": 5, "secret_threshold": 3}, &initialized) != 200 || len(initialized.Keys) != 5 || initialized.RootToken == "" {
		t.Fatal("native 5/3 initialization failed")
	}
	token = initialized.RootToken
	unseal := func(n node) {
		t.Helper()
		wait("sealed listener ready", func() bool {
			var state struct {
				Sealed bool `json:"sealed"`
			}
			return request(n.address, "GET", "sys/seal-status", nil, &state) == 200 && state.Sealed
		})
		for i := 0; i < 2; i++ {
			var state struct {
				Sealed bool `json:"sealed"`
			}
			if request(n.address, "PUT", "sys/unseal", map[string]string{"key": initialized.Keys[i]}, &state) != 200 || !state.Sealed {
				t.Fatal("native seal opened without threshold")
			}
		}
		if request(n.address, "PUT", "sys/unseal", map[string]string{"key": initialized.Keys[2]}, nil) != 200 {
			t.Fatal("original Shamir quorum could not unseal")
		}
		wait("unsealed original cluster", func() bool {
			var s struct {
				Sealed      bool `json:"sealed"`
				Initialized bool `json:"initialized"`
			}
			return request(n.address, "GET", "sys/seal-status", nil, &s) == 200 && s.Initialized && !s.Sealed
		})
	}
	unseal(leader)
	var state struct {
		ClusterID string `json:"cluster_id"`
	}
	if request(leader.address, "GET", "sys/seal-status", nil, &state) != 200 || state.ClusterID == "" {
		t.Fatal("cluster identity unavailable")
	}
	if request(leader.address, "POST", "sys/mounts/qualification", map[string]string{"type": "kv"}, nil) != 204 {
		t.Fatal("native marker mount failed")
	}
	put := func(n node, value string) bool {
		return request(n.address, "POST", "qualification/marker", map[string]string{"value": value}, nil) == 204
	}
	get := func(n node, expected string) bool {
		var result struct {
			Data struct {
				Value string `json:"value"`
			} `json:"data"`
		}
		return request(n.address, "GET", "qualification/marker", nil, &result) == 200 && result.Data.Value == expected
	}
	if !put(leader, "before-migration") {
		t.Fatal("native marker write failed")
	}
	// Isolated fixture CA only: verify preservation of actual PKI issuer and
	// revocation state, rather than accepting a generic KV restore as PKI proof.
	if request(leader.address, "POST", "sys/mounts/qualification-pki", map[string]string{"type": "pki"}, nil) != 204 {
		t.Fatal("native PKI mount failed")
	}
	var root struct {
		Data struct {
			Certificate string `json:"certificate"`
			IssuerID    string `json:"issuer_id"`
		} `json:"data"`
	}
	if request(leader.address, "POST", "qualification-pki/root/generate/internal", map[string]any{"common_name": "isolated qualification CA", "ttl": "1h", "key_type": "ec", "key_bits": 256}, &root) != 200 || root.Data.Certificate == "" || root.Data.IssuerID == "" {
		t.Fatal("isolated native PKI fixture unavailable")
	}
	if request(leader.address, "POST", "qualification-pki/roles/native", map[string]any{"allowed_domains": []string{"native.example"}, "allow_bare_domains": true, "max_ttl": "10m"}, nil) != 200 {
		t.Fatal("native PKI fixture role unavailable")
	}
	var issued struct {
		Data struct {
			Serial string `json:"serial_number"`
		} `json:"data"`
	}
	if request(leader.address, "POST", "qualification-pki/issue/native", map[string]string{"common_name": "native.example", "ttl": "5m"}, &issued) != 200 || issued.Data.Serial == "" {
		t.Fatal("native PKI fixture issuance failed")
	}
	if request(leader.address, "POST", "qualification-pki/revoke", map[string]string{"serial_number": issued.Data.Serial}, nil) != 200 {
		t.Fatal("native PKI fixture revocation failed")
	}
	verifyPKI := func(n node) bool {
		var ca, crl struct {
			Data struct {
				Certificate string `json:"certificate"`
			} `json:"data"`
		}
		if request(n.address, "GET", "qualification-pki/cert/ca", nil, &ca) != 200 || ca.Data.Certificate != root.Data.Certificate || request(n.address, "GET", "qualification-pki/cert/crl", nil, &crl) != 200 {
			return false
		}
		block, _ := pem.Decode([]byte(crl.Data.Certificate))
		if block == nil {
			return false
		}
		list, err := x509.ParseRevocationList(block.Bytes)
		if err != nil {
			return false
		}
		rootBlock, _ := pem.Decode([]byte(root.Data.Certificate))
		if rootBlock == nil {
			return false
		}
		rootCert, err := x509.ParseCertificate(rootBlock.Bytes)
		if err != nil || list.CheckSignatureFrom(rootCert) != nil {
			return false
		}
		serial := new(big.Int)
		if _, ok := serial.SetString(strings.ReplaceAll(issued.Data.Serial, ":", ""), 16); !ok {
			return false
		}
		for _, entry := range list.RevokedCertificateEntries {
			if entry.SerialNumber.Cmp(serial) == 0 {
				return true
			}
		}
		return false
	}
	if !verifyPKI(leader) {
		t.Fatal("native revocation fixture could not be verified")
	}
	// Offline native migration copies encrypted storage and the existing seal.
	// The destination is a new empty volume and is never initialized separately.
	docker("stop", "-t", "10", leader.name)
	docker("rm", leader.name)
	raftVolume := volume("raft-data-0")
	migration := `storage_source "file" {
  path = "/source"
}
storage_destination "raft" {
  path = "/destination"
  node_id = "node-0"
}
cluster_addr = "https://node-0:8201"
`
	if err := os.WriteFile(filepath.Join(directory, "migrate.hcl"), []byte(migration), 0600); err != nil {
		t.Fatal(err)
	}
	docker("run", "--pull=never", "--rm", "--network", prefix, "--user", "0:0", "-v", directory+":/fixture:ro", "-v", fileVolume+":/source", "-v", raftVolume+":/destination", "--entrypoint", "bao", image, "operator", "migrate", "-config=/fixture/migrate.hcl")
	leader = start(0, "raft", raftVolume)
	unseal(leader)
	wait("migrated data", func() bool { return get(leader, "before-migration") })
	if !verifyPKI(leader) {
		t.Fatal("file-to-Raft migration changed CA or revocation state")
	}
	var migrated struct {
		ClusterID string `json:"cluster_id"`
		Storage   string `json:"storage_type"`
		Shares    int    `json:"n"`
		Threshold int    `json:"t"`
	}
	if request(leader.address, "GET", "sys/seal-status", nil, &migrated) != 200 || migrated.ClusterID != state.ClusterID || migrated.Storage != "raft" || migrated.Shares != 5 || migrated.Threshold != 3 {
		t.Fatal("migration changed cluster or seal identity")
	}
	t.Log("offline file-to-Raft migration retained original data, cluster identity and 5/3 seal")
	nodes := []node{leader}
	for i := 1; i < 3; i++ {
		n := start(i, "raft", volume(fmt.Sprintf("raft-data-%d", i)))
		wait("peer join challenge", func() bool {
			return request(n.address, "POST", "sys/storage/raft/join", map[string]string{"leader_api_addr": "https://node-0:8200", "leader_ca_cert": string(caPEM), "leader_tls_servername": "openbao-internal"}, nil) == 200
		})
		unseal(n)
		nodes = append(nodes, n)
	}
	peers := func(n node) bool {
		var result struct {
			Data struct {
				Config struct {
					Servers []struct {
						Voter bool `json:"voter"`
					} `json:"servers"`
				} `json:"config"`
			} `json:"data"`
		}
		if request(n.address, "GET", "sys/storage/raft/configuration", nil, &result) != 200 || len(result.Data.Config.Servers) != 3 {
			return false
		}
		for _, server := range result.Data.Config.Servers {
			if !server.Voter {
				return false
			}
		}
		return true
	}
	wait("three voting peers", func() bool { return peers(leader) })
	t.Log("three original-seal Raft voters joined over TLS")
	// Stop the original leader. Writes must continue on the surviving quorum.
	docker("stop", "-t", "10", leader.name)
	active := node{}
	wait("leader fault quorum", func() bool {
		for _, n := range nodes[1:] {
			var s struct {
				HA   bool `json:"ha_enabled"`
				Self bool `json:"is_self"`
			}
			if request(n.address, "GET", "sys/leader", nil, &s) == 200 && s.HA && s.Self {
				active = n
				return true
			}
		}
		return false
	})
	if !put(active, "after-leader-fault") {
		t.Fatal("surviving quorum cannot write")
	}
	docker("start", leader.name)
	// Docker may allocate another dynamic loopback port when a container starts
	// again. Refresh only this known fixture endpoint, never environment DNS.
	port := strings.TrimSpace(string(docker("port", leader.name, "8200/tcp")))
	if !strings.HasPrefix(port, "127.0.0.1:") || strings.ContainsAny(port, "\r\n") {
		t.Fatal("restarted native drill port is not isolated")
	}
	leader.address = "https://" + port
	nodes[0] = leader
	unseal(leader)
	wait("restarted original peer", func() bool { return get(leader, "after-leader-fault") && peers(active) })
	t.Log("surviving quorum wrote successfully and restarted peer retained its identity")
	// Exercise the maintained snapshot adapter, encrypted age transport and
	// normal same-seal restore. No force endpoint or new Root is used.
	tokenFile := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	source := OpenBaoRaftSource{active.address, filepath.Join(directory, "ca.crt"), fingerprint, tokenFile, state.ClusterID}
	component := Component{ID: "openbao", Kind: "openbao-raft", OpenBao: &source}
	engine := Engine{Config: Config{MaxArchiveBytes: 64 << 20}}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	writer, err := age.Encrypt(&encrypted, identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.captureOpenBaoRaft(ctx, component, writer); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if !put(active, "after-snapshot") {
		t.Fatal("snapshot mutation failed")
	}
	reader, err := age.Decrypt(bytes.NewReader(encrypted.Bytes()), identity)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(directory, "snapshot")
	f, err := os.OpenFile(snapshot, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(f, reader)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal("snapshot decryption failed")
	}
	if err := engine.restoreOpenBaoRaft(ctx, component, snapshot); err != nil {
		t.Fatal(err)
	}
	wait("same-seal snapshot state", func() bool { return get(active, "after-leader-fault") })
	if !verifyPKI(active) {
		t.Fatal("snapshot restore changed CA or revocation state")
	}
	for _, n := range nodes {
		var s struct {
			ClusterID string `json:"cluster_id"`
		}
		if request(n.address, "GET", "sys/seal-status", nil, &s) != 200 || s.ClusterID != state.ClusterID {
			t.Fatal("restore changed original cluster identity")
		}
	}
	var audits struct {
		Data map[string]struct {
			Options map[string]string `json:"options"`
		} `json:"data"`
	}
	if request(active.address, "GET", "sys/audit", nil, &audits) != 200 || audits.Data["deployment-audit/"].Options["file_path"] != "/openbao/audit/audit.jsonl" || audits.Data["deployment-audit/"].Options["log_raw"] != "false" {
		t.Fatal("native durable audit device is not enabled")
	}
	// Only an active peer must have recorded authenticated requests. Standby
	// audit storage need not already contain an event before becoming active.
	docker("exec", active.name, "test", "-s", "/openbao/audit/audit.jsonl")
	t.Log("native TLS, 5/3 seal, offline file-to-Raft migration, three voters, leader fault, restart and encrypted original-seal snapshot restore verified; production custody and full security-state qualification remain separate")
}

func nativeBaoTLS(t *testing.T) ([]byte, []byte, []byte, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated OpenBao drill CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "openbao-internal"}, DNSNames: []string{"openbao-internal", "node-0", "node-1", "node-2"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	fp := sha256.Sum256(caDER)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), hex.EncodeToString(fp[:])
}
