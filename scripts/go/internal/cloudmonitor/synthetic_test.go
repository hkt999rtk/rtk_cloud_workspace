package cloudmonitor

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/golang-jwt/jwt/v5"
	"github.com/pion/stun/v3"
	"github.com/pion/turn/v4"
)

func syntheticFixture(t *testing.T, credentials syntheticCredentials) (Runtime, string) {
	t.Helper()
	rt := Runtime{ConfigRoot: t.TempDir()}
	path := filepath.Join(rt.ConfigRoot, "identity.json")
	body, _ := json.Marshal(credentials)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return rt, "identity.json"
}

func TestSyntheticAuthRotationPersistsBeforeProtectedRead(t *testing.T) {
	now := time.Now().UTC()
	rt, reference := syntheticFixture(t, syntheticCredentials{Dedicated: true, Email: "monitor@example.test", Password: "secret-password"})
	loginCount, refreshCount := 0, 0
	protectedFails := false
	currentGrant := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		tokens := func(access, refresh string) {
			json.NewEncoder(w).Encode(map[string]any{"tokens": syntheticTokens{AccessToken: access, AccessTokenExpiresAt: now.Add(15 * time.Minute), RefreshToken: refresh, RefreshTokenExpiresAt: now.Add(30 * 24 * time.Hour)}})
		}
		switch req.URL.Path {
		case "/v1/auth/login":
			var payload map[string]string
			if json.NewDecoder(req.Body).Decode(&payload) != nil || payload["email"] != "monitor@example.test" || payload["password"] != "secret-password" || len(payload) != 2 {
				t.Errorf("unexpected login payload")
			}
			loginCount++
			currentGrant = "secret-initial-refresh"
			tokens("secret-initial-access", currentGrant)
		case "/v1/auth/refresh":
			var payload map[string]string
			json.NewDecoder(req.Body).Decode(&payload)
			if payload["refresh_token"] != currentGrant {
				t.Errorf("consumed grant reused")
				w.WriteHeader(401)
				return
			}
			refreshCount++
			currentGrant = fmt.Sprintf("secret-refresh-%d", refreshCount)
			tokens(fmt.Sprintf("secret-access-%d", refreshCount), currentGrant)
		case "/v1/me":
			if req.Header.Get("Authorization") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if req.Header.Get("Authorization") != fmt.Sprintf("Bearer secret-access-%d", refreshCount) {
				t.Error("protected read did not use the rotated token")
			}
			if protectedFails {
				w.WriteHeader(500)
				fmt.Fprint(w, `{"error":"secret-server-payload"}`)
				return
			}
			fmt.Fprint(w, `{"user_id":"dedicated-monitor"}`)
		default:
			t.Errorf("unexpected mutation endpoint: %s", req.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	probe := AuthProbe{Name: "dedicated", Kind: "account-manager", LoginURL: server.URL + "/v1/auth/login", RefreshURL: server.URL + "/v1/auth/refresh", ValidationURL: server.URL + "/v1/me", CredentialsFile: reference}
	cfg := Config{TimeoutSeconds: 2}
	first := probeSyntheticAuth(context.Background(), cfg, rt, probe, now)
	if first.Status != Pass || first.ExpiresAt == nil || loginCount != 1 || refreshCount != 1 {
		t.Fatalf("authentication/rotation failed: %+v", first)
	}
	statePath := syntheticAuthPath(rt, probe)
	info, err := os.Stat(statePath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("rotation state not private: %v", err)
	}
	protectedFails = true
	second := probeSyntheticAuth(context.Background(), cfg, rt, probe, now.Add(time.Minute))
	if second.Status != Fail || loginCount != 1 || refreshCount != 2 {
		t.Fatalf("existing state not used or protected failure ignored: %+v", second)
	}
	state, exists, err := readSyntheticState(rt, probe)
	if err != nil || !exists || state.Tokens.RefreshToken != currentGrant {
		t.Fatal("new grant must remain saved even when subsequent read fails")
	}
	protectedFails = false
	third := probeSyntheticAuth(context.Background(), cfg, rt, probe, now.Add(2*time.Minute))
	if third.Status != Pass || loginCount != 1 || refreshCount != 3 {
		t.Fatalf("rotation recovery failed: %+v", third)
	}
	resultJSON, _ := json.Marshal([]Result{first, second, third})
	for _, secret := range []string{"secret-password", "secret-access", "secret-refresh", "secret-server-payload"} {
		if bytes.Contains(resultJSON, []byte(secret)) {
			t.Errorf("report includes secret %s", secret)
		}
	}
}

func TestSyntheticAuthPublicValidationStopsBeforeRotation(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusNoContent, http.StatusFound, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			rt, reference := syntheticFixture(t, syntheticCredentials{Dedicated: true, Email: "monitor@example.test", Password: "secret"})
			mutations := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/v1/me" {
					mutations++
				}
				if req.Header.Get("Authorization") != "" || req.Method != http.MethodGet {
					t.Error("control sent credentials or made a mutation")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				fmt.Fprint(w, `{"public":true}`)
			}))
			defer server.Close()
			probe := AuthProbe{Name: "public", Kind: "account-manager", LoginURL: server.URL + "/v1/auth/login", RefreshURL: server.URL + "/v1/auth/refresh", ValidationURL: server.URL + "/v1/me", CredentialsFile: reference}
			r := probeSyntheticAuth(context.Background(), Config{TimeoutSeconds: 1}, rt, probe, time.Now())
			if r.Status != Unknown || mutations != 0 {
				t.Fatalf("unprotected endpoint permitted authentication: %+v, mutations=%d", r, mutations)
			}
			if _, err := os.Stat(syntheticAuthPath(rt, probe)); !os.IsNotExist(err) {
				t.Fatal("control failure wrote token state")
			}
		})
	}
}

func TestSyntheticRequiresDedicatedIdentityAndExplicitConfig(t *testing.T) {
	rt, reference := syntheticFixture(t, syntheticCredentials{Dedicated: false, Email: "someone@example.test", Password: "secret"})
	probe := AuthProbe{Name: "shared", Kind: "account-manager", CredentialsFile: reference}
	if r := probeSyntheticAuth(context.Background(), Config{}, rt, probe, time.Now()); r.Status != Unknown {
		t.Fatal("generic account permitted")
	}
	missing := CollectSynthetic(context.Background(), Config{}, rt, nil, time.Now())
	if len(missing.Results) != 3 {
		t.Fatal("missing probe classes were hidden")
	}
	for _, result := range missing.Results {
		if result.Status != NotRun || !result.Required {
			t.Fatal("missing dedicated probe must reduce coverage")
		}
	}
	if _, err := os.Stat(filepath.Join(rt.ConfigRoot, "monitor")); !os.IsNotExist(err) {
		t.Fatal("unconfigured probes created state")
	}
	lock, err := acquireSyntheticLock(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if second, err := acquireSyntheticLock(rt); err == nil {
		second.Close()
		t.Fatal("two concurrent groups obtained the same state lock")
	}
}

func TestSyntheticVideoMTLSReauthenticationAndReissue(t *testing.T) {
	for _, tc := range []struct {
		name             string
		claims           jwt.MapClaims
		wantReissue      int
		publicValidation bool
	}{
		{name: "legacy", wantReissue: 2},
		{name: "product-revision", claims: jwt.MapClaims{"product_service_revision": 4, "entitlement_revision": 9}},
		{name: "entitlement-revision", claims: jwt.MapClaims{"entitlement_revision": 2}},
		{name: "test-lab", claims: jwt.MapClaims{"actor_type": "test_lab"}},
		{name: "certificate-only-validation", publicValidation: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testSyntheticVideoAuth(t, tc.claims, tc.wantReissue, tc.publicValidation)
		})
	}
}

func testSyntheticVideoAuth(t *testing.T, extraClaims jwt.MapClaims, wantReissue int, publicValidation bool) {
	t.Helper()
	now := time.Now().UTC()
	bootstrap, reissue := 0, 0
	current := ""
	newToken := func() string {
		claims := jwt.MapClaims{"exp": now.Add(10 * time.Minute).Unix(), "scope": "device", "subject_id": "monitor-device", "jti": syntheticID()}
		for key, value := range extraClaims {
			claims[key] = value
		}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("server-only-key"))
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.TLS == nil || len(req.TLS.PeerCertificates) != 1 {
			t.Error("bootstrap did not use mTLS")
		}
		var payload map[string]string
		switch req.URL.Path {
		case "/request_token":
			json.NewDecoder(req.Body).Decode(&payload)
			if payload["scope"] != "device" || payload["devid"] != "monitor-device" || req.Header.Get("Authorization") != "" {
				t.Error("bootstrap payload differs from device mTLS contract")
			}
			bootstrap++
			current = newToken()
			json.NewEncoder(w).Encode(map[string]string{"access_token": current, "token_type": "bearer"})
		case "/refresh_token":
			if wantReissue == 0 {
				t.Error("revisioned/test-lab token attempted forbidden stateless refresh")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			json.NewDecoder(req.Body).Decode(&payload)
			if payload["refresh_token"] != current {
				t.Error("reissue did not carry current signed token")
			}
			reissue++
			current = newToken()
			json.NewEncoder(w).Encode(map[string]string{"access_token": current, "token_type": "bearer"})
		case "/api/device-info":
			if req.Header.Get("Authorization") == "" && !publicValidation {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if publicValidation {
				fmt.Fprint(w, `{"devid":"monitor-device"}`)
				return
			}
			if req.Header.Get("Authorization") != "Bearer "+current {
				t.Error("updated Video token was not validated")
			}
			fmt.Fprint(w, `{"devid":"monitor-device"}`)
		default:
			w.WriteHeader(404)
		}
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	rt, reference := syntheticFixture(t, syntheticCredentials{Dedicated: true, ClientCertFile: "client.crt", ClientKeyFile: "client.key", Scope: "device", DeviceID: "monitor-device"})
	certificate := server.TLS.Certificates[0]
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]})
	keyDER, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	for name, contents := range map[string][]byte{"client.crt": certPEM, "client.key": keyPEM, "server-ca.crt": certPEM} {
		if err := os.WriteFile(filepath.Join(rt.ConfigRoot, name), contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	probe := AuthProbe{Name: "device", Kind: "video-cloud", LoginURL: server.URL + "/request_token", RefreshURL: server.URL + "/refresh_token", ValidationURL: server.URL + "/api/device-info", CredentialsFile: reference, CAFile: "server-ca.crt"}
	for i := 0; i < 2; i++ {
		r := probeSyntheticAuth(context.Background(), Config{TimeoutSeconds: 2}, rt, probe, now)
		if publicValidation {
			if r.Status != Unknown || bootstrap != 0 || reissue != 0 {
				t.Fatalf("certificate-only endpoint permitted bearer validation: %+v", r)
			}
			if _, err := os.Stat(syntheticAuthPath(rt, probe)); !os.IsNotExist(err) {
				t.Fatal("certificate-only endpoint wrote token state")
			}
		} else if r.Status != Pass || r.ExpiresAt == nil {
			t.Fatalf("Video mTLS flow failed: %+v", r)
		}
	}
	if !publicValidation && (bootstrap != 2 || reissue != wantReissue) {
		t.Fatal("Video probe did not exercise certificate-based recovery each round")
	}
}

type syntheticFakeToken struct{ err error }

func (t syntheticFakeToken) Wait() bool                     { return true }
func (t syntheticFakeToken) WaitTimeout(time.Duration) bool { return true }
func (t syntheticFakeToken) Done() <-chan struct{}          { ch := make(chan struct{}); close(ch); return ch }
func (t syntheticFakeToken) Error() error                   { return t.err }

type syntheticFakeMessage struct {
	topic    string
	payload  []byte
	retained bool
}

func (m syntheticFakeMessage) Duplicate() bool   { return false }
func (m syntheticFakeMessage) Qos() byte         { return 1 }
func (m syntheticFakeMessage) Retained() bool    { return m.retained }
func (m syntheticFakeMessage) Topic() string     { return m.topic }
func (m syntheticFakeMessage) MessageID() uint16 { return 1 }
func (m syntheticFakeMessage) Payload() []byte   { return m.payload }
func (m syntheticFakeMessage) Ack()              {}

type syntheticFakeMQTT struct {
	handler                    mqtt.MessageHandler
	topic                      string
	connectErr, unsubscribeErr error
	unsubscribed, disconnected int
	retained                   bool
}

func (c *syntheticFakeMQTT) Connect() mqtt.Token { return syntheticFakeToken{c.connectErr} }
func (c *syntheticFakeMQTT) Subscribe(topic string, qos byte, callback mqtt.MessageHandler) mqtt.Token {
	c.topic = topic
	c.handler = callback
	return syntheticFakeToken{}
}
func (c *syntheticFakeMQTT) Publish(topic string, qos byte, retained bool, payload any) mqtt.Token {
	c.retained = retained
	if c.handler != nil {
		c.handler(nil, syntheticFakeMessage{topic: topic, payload: payload.([]byte)})
	}
	return syntheticFakeToken{}
}
func (c *syntheticFakeMQTT) Unsubscribe(topics ...string) mqtt.Token {
	c.unsubscribed++
	return syntheticFakeToken{c.unsubscribeErr}
}
func (c *syntheticFakeMQTT) Disconnect(uint) { c.disconnected++ }

func TestSyntheticMQTTCorrelationAndCleanup(t *testing.T) {
	rt, reference := syntheticFixture(t, syntheticCredentials{Dedicated: true, Username: "dedicated-user", Password: "secret-mqtt", ClientIDPrefix: "monitor-"})
	probe := MQTTProbe{Name: "dedicated", Broker: "tcp://127.0.0.1:1883", Topic: "monitor/dev", CredentialsFile: reference}
	client := &syntheticFakeMQTT{}
	var options *mqtt.ClientOptions
	factory := func(opts *mqtt.ClientOptions) syntheticMQTTClient { options = opts; return client }
	r, cleanup := probeSyntheticMQTTWithFactory(context.Background(), Config{TimeoutSeconds: 1}, rt, probe, time.Now(), factory)
	if r.Status != Pass || cleanup.Status != Pass || client.unsubscribed != 1 || client.disconnected != 1 || client.retained || !strings.HasPrefix(client.topic, "monitor/dev/") || strings.ContainsAny(client.topic, "+#") || !options.CleanSession || options.AutoReconnect || !strings.HasPrefix(options.ClientID, "monitor-") {
		t.Fatalf("MQTT flow/cleanup failed: %+v %+v", r, cleanup)
	}
	oldTopic, oldID := client.topic, options.ClientID
	client = &syntheticFakeMQTT{unsubscribeErr: errors.New("secret-mqtt")}
	r, cleanup = probeSyntheticMQTTWithFactory(context.Background(), Config{TimeoutSeconds: 1}, rt, probe, time.Now(), factory)
	if r.Status != Pass || cleanup.Status != Warn || client.disconnected != 1 || client.topic == oldTopic || options.ClientID == oldID {
		t.Fatal("MQTT uniqueness or failed cleanup missing")
	}
	encoded, _ := json.Marshal([]Result{r, cleanup})
	if bytes.Contains(encoded, []byte("secret-mqtt")) {
		t.Fatal("MQTT errors expose credentials")
	}
	client = &syntheticFakeMQTT{connectErr: errors.New("secret-mqtt")}
	r, cleanup = probeSyntheticMQTTWithFactory(context.Background(), Config{TimeoutSeconds: 1}, rt, probe, time.Now(), factory)
	if r.Status != Fail || client.unsubscribed != 0 || client.disconnected != 1 {
		t.Fatal("failed MQTT connect was not cleaned")
	}
}

func TestSyntheticMQTTCancellationClosesPendingHandshake(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	closed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			close(closed)
			return
		}
		defer conn.Close()
		buffer := make([]byte, 2048)
		for {
			if _, err := conn.Read(buffer); err != nil {
				close(closed)
				return
			}
		}
	}()
	rt, reference := syntheticFixture(t, syntheticCredentials{Dedicated: true, Username: "monitor", Password: "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	r, cleanup := probeSyntheticMQTT(ctx, Config{TimeoutSeconds: 5}, rt, MQTTProbe{Name: "blackhole", Broker: "tcp://" + listener.Addr().String(), Topic: "monitor/dev", CredentialsFile: reference}, time.Now())
	if r.Status != Fail || cleanup.Status != Pass || time.Since(start) > time.Second {
		t.Fatalf("MQTT did not honor group deadline: %+v %+v", r, cleanup)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("MQTT handshake socket remained open after probe returned")
	}
}

func TestSyntheticTURNActualRelayAndCleanup(t *testing.T) {
	for _, network := range []string{"udp", "tcp"} {
		t.Run(network, func(t *testing.T) { testSyntheticTURNRelay(t, network) })
	}
}

func testSyntheticTURNRelay(t *testing.T, network string) {
	const realm = "monitor.test"
	config := turn.ServerConfig{Realm: realm, LoggerFactory: silentTURNLogger(), AuthHandler: func(username, gotRealm string, _ net.Addr) ([]byte, bool) {
		if username != "dedicated-monitor" || gotRealm != realm {
			return nil, false
		}
		return turn.GenerateAuthKey(username, realm, "secret-turn"), true
	}}
	relayGenerator := &turn.RelayAddressGeneratorStatic{RelayAddress: net.ParseIP("127.0.0.1"), Address: "127.0.0.1"}
	var address string
	if network == "udp" {
		control, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer control.Close()
		address = control.LocalAddr().String()
		config.PacketConnConfigs = []turn.PacketConnConfig{{PacketConn: control, RelayAddressGenerator: relayGenerator}}
	} else {
		control, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer control.Close()
		address = control.Addr().String()
		config.ListenerConfigs = []turn.ListenerConfig{{Listener: control, RelayAddressGenerator: relayGenerator}}
	}
	server, err := turn.NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	peer, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, address, e := peer.ReadFrom(buffer)
			if e != nil {
				return
			}
			request := &stun.Message{Raw: append([]byte(nil), buffer[:n]...)}
			if request.Decode() != nil || request.Type != stun.BindingRequest {
				continue
			}
			udp := address.(*net.UDPAddr)
			response, e := stun.Build(stun.NewTransactionIDSetter(request.TransactionID), stun.BindingSuccess, &stun.XORMappedAddress{IP: udp.IP, Port: udp.Port})
			if e == nil {
				peer.WriteTo(response.Raw, address)
			}
		}
	}()
	rt, ref := syntheticFixture(t, syntheticCredentials{Dedicated: true, Username: "dedicated-monitor", Password: "secret-turn"})
	probe := TURNProbe{Name: "local", Address: address, Network: network, Realm: realm, PeerAddress: peer.LocalAddr().String(), CredentialsFile: ref}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, cleanup := probeSyntheticTURN(ctx, Config{TimeoutSeconds: 1}, rt, probe, time.Now())
	if r.Status != Pass || cleanup.Status != Pass {
		t.Fatalf("actual TURN relay failed: %+v %+v", r, cleanup)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for server.AllocationCount() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if server.AllocationCount() != 0 {
		t.Fatal("TURN allocation remained after probe cleanup")
	}
	encoded, _ := json.Marshal([]Result{r, cleanup})
	if bytes.Contains(encoded, []byte("secret-turn")) {
		t.Fatal("TURN credentials in results")
	}
}

func TestSyntheticTURNCancellationDoesNotWaitForRetransmissions(t *testing.T) {
	blackhole, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blackhole.Close()
	rt, ref := syntheticFixture(t, syntheticCredentials{Dedicated: true, Username: "monitor", Password: "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	r, _ := probeSyntheticTURN(ctx, Config{TimeoutSeconds: 1}, rt, TURNProbe{Name: "blackhole", Address: blackhole.LocalAddr().String(), Network: "udp", Realm: "test", PeerAddress: "127.0.0.1:3478", CredentialsFile: ref}, time.Now())
	if r.Status != Fail || time.Since(start) > time.Second {
		t.Fatalf("TURN ignored group deadline: %+v", r)
	}
}
