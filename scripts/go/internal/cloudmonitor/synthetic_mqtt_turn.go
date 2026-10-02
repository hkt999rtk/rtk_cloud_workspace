package cloudmonitor

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/turn/v4"
)

type syntheticMQTTClient interface {
	Connect() mqtt.Token
	Subscribe(string, byte, mqtt.MessageHandler) mqtt.Token
	Publish(string, byte, bool, any) mqtt.Token
	Unsubscribe(...string) mqtt.Token
	Disconnect(uint)
}

type mqttProbeFactory func(*mqtt.ClientOptions) syntheticMQTTClient

func waitSyntheticMQTT(ctx context.Context, token mqtt.Token) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-token.Done():
		return token.Error()
	}
}

func probeSyntheticMQTT(ctx context.Context, cfg Config, rt Runtime, probe MQTTProbe, now time.Time) (Result, Result) {
	return probeSyntheticMQTTWithFactory(ctx, cfg, rt, probe, now, func(options *mqtt.ClientOptions) syntheticMQTTClient { return mqtt.NewClient(options) })
}

func probeSyntheticMQTTWithFactory(ctx context.Context, cfg Config, rt Runtime, probe MQTTProbe, now time.Time, factory mqttProbeFactory) (r Result, cleanup Result) {
	r = result("mqtt", "synthetic/mqtt/"+probe.Name, "functional", true, now)
	cleanup = result("mqtt", "synthetic/mqtt/"+probe.Name+"/cleanup", "functional", true, now)
	cleanup.Status = NotApplicable
	cleanup.Reason = "尚未建立 MQTT 測試連線"
	start := time.Now()
	defer func() { r.DurationMS = time.Since(start).Milliseconds() }()
	if ctx.Err() != nil {
		r.Status = NotRun
		r.Reason = "功能測試執行期限已到"
		return
	}
	credentials, err := readSyntheticCredentials(rt, probe.CredentialsFile)
	if err != nil {
		r.Reason = "缺少標記 dedicated=true 的專用 MQTT 身分"
		return
	}
	broker, err := url.Parse(probe.Broker)
	if err != nil || broker.Host == "" || broker.User != nil || (broker.Scheme != "tcp" && broker.Scheme != "ssl" && broker.Scheme != "tls" && broker.Scheme != "mqtt" && broker.Scheme != "mqtts") || broker.Path != "" || broker.RawQuery != "" || broker.Fragment != "" {
		r.Reason = "MQTT broker 需為 TCP/TLS 位址且不含 credential"
		return
	}
	if strings.ContainsAny(probe.Topic, "+#\x00") || strings.Trim(probe.Topic, "/") == "" {
		r.Reason = "MQTT 測試需要明確授權的監看 topic 前綴"
		return
	}
	clientIDPrefix := credentials.ClientIDPrefix
	if clientIDPrefix == "" {
		clientIDPrefix = "cloud-monitor-"
	}
	if strings.ContainsAny(clientIDPrefix, "\x00\r\n") || len(clientIDPrefix) > 80 {
		r.Reason = "MQTT 專用 client ID 前綴無效"
		return
	}
	runID := syntheticID()
	if runID == "" {
		r.Reason = "MQTT 測試無法建立唯一識別"
		return
	}
	topic := strings.TrimRight(probe.Topic, "/") + "/" + runID
	payload := []byte("cloud-monitor:" + runID)
	options := mqtt.NewClientOptions().AddBroker(probe.Broker).SetClientID(clientIDPrefix + runID).SetCleanSession(true).SetAutoReconnect(false).SetConnectRetry(false).SetProtocolVersion(4).SetConnectTimeout(timeout(cfg)).SetWriteTimeout(timeout(cfg)).SetKeepAlive(15 * time.Second)
	options.SetUsername(credentials.Username)
	password := credentials.Password
	if password == "" {
		password = credentials.AccessToken
	}
	options.SetPassword(password)
	if broker.Scheme == "ssl" || broker.Scheme == "tls" || broker.Scheme == "mqtts" {
		httpClient, err := HTTPClient(probe.CAFile, credentials.ClientCertFile, credentials.ClientKeyFile, rt, timeout(cfg))
		if err != nil {
			r.Reason = "MQTT TLS trust 或專用憑證設定無效"
			return r, cleanup
		}
		options.SetTLSConfig(httpClient.Transport.(*http.Transport).TLSClientConfig.Clone())
	}
	// Paho Connect is asynchronous. Own the socket and its context so a group
	// deadline cannot leave an in-progress dial/handshake behind after return.
	networkCtx, cancelNetwork := context.WithCancel(ctx)
	var socketMu sync.Mutex
	var socket net.Conn
	options.SetCustomOpenConnectionFn(func(endpoint *url.URL, opts mqtt.ClientOptions) (net.Conn, error) {
		dialer := &net.Dialer{Timeout: timeout(cfg)}
		var conn net.Conn
		var err error
		if opts.TLSConfig != nil {
			config := opts.TLSConfig.Clone()
			if config.ServerName == "" {
				config.ServerName = endpoint.Hostname()
			}
			conn, err = (&tls.Dialer{NetDialer: dialer, Config: config}).DialContext(networkCtx, "tcp", endpoint.Host)
		} else {
			conn, err = dialer.DialContext(networkCtx, "tcp", endpoint.Host)
		}
		if err != nil {
			return nil, err
		}
		socketMu.Lock()
		defer socketMu.Unlock()
		if networkCtx.Err() != nil {
			conn.Close()
			return nil, networkCtx.Err()
		}
		socket = conn
		return conn, nil
	})
	closeSocket := func() {
		socketMu.Lock()
		defer socketMu.Unlock()
		if socket != nil {
			_ = socket.Close()
		}
	}
	stopSocketClose := context.AfterFunc(networkCtx, closeSocket)
	defer stopSocketClose()
	client := factory(options)
	connected := false
	defer func() {
		cleanup.Status = Pass
		cleanup.Reason = "已關閉 clean-session MQTT 連線"
		if connected {
			cleanCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			if err := waitSyntheticMQTT(cleanCtx, client.Unsubscribe(topic)); err != nil {
				cleanup.Status = Warn
				cleanup.Reason = "取消訂閱未確認；已關閉 clean-session 連線"
			}
			cancel()
		}
		cancelNetwork()
		closeSocket()
		client.Disconnect(0)
	}()
	if err := waitSyntheticMQTT(ctx, client.Connect()); err != nil {
		r.Status = Fail
		r.Reason = "專用 MQTT 身分連線／認證失敗"
		return
	}
	connected = true
	matched := make(chan struct{}, 1)
	if err := waitSyntheticMQTT(ctx, client.Subscribe(topic, 1, func(_ mqtt.Client, message mqtt.Message) {
		if !message.Retained() && message.Topic() == topic && bytes.Equal(message.Payload(), payload) {
			select {
			case matched <- struct{}{}:
			default:
			}
		}
	})); err != nil {
		r.Status = Fail
		r.Reason = "MQTT 專用監看 topic 訂閱失敗"
		return
	}
	if err := waitSyntheticMQTT(ctx, client.Publish(topic, 1, false, payload)); err != nil {
		r.Status = Fail
		r.Reason = "MQTT 非 retained 測試訊息發送失敗"
		return
	}
	select {
	case <-ctx.Done():
		r.Status = Fail
		r.Reason = "MQTT correlation 訊息往返逾時"
	case <-matched:
		r.Status = Pass
		r.Reason = "MQTT 唯一 client 與專用 topic 訊息往返成功"
	}
	return
}

func silentTURNLogger() logging.LoggerFactory {
	return &logging.DefaultLoggerFactory{Writer: io.Discard, DefaultLogLevel: logging.LogLevelDisabled}
}

func probeSyntheticTURN(ctx context.Context, cfg Config, rt Runtime, probe TURNProbe, now time.Time) (r Result, cleanup Result) {
	r = result("turn", "synthetic/turn/"+probe.Name, "functional", true, now)
	cleanup = result("turn", "synthetic/turn/"+probe.Name+"/cleanup", "functional", true, now)
	cleanup.Status = NotApplicable
	cleanup.Reason = "尚未建立 TURN allocation"
	start := time.Now()
	defer func() { r.DurationMS = time.Since(start).Milliseconds() }()
	if ctx.Err() != nil {
		r.Status = NotRun
		r.Reason = "功能測試執行期限已到"
		return
	}
	credentials, err := readSyntheticCredentials(rt, probe.CredentialsFile)
	if err != nil || credentials.Username == "" || credentials.Password == "" {
		r.Reason = "缺少標記 dedicated=true 的專用 TURN credentials"
		return
	}
	if probe.Network != "udp" && probe.Network != "tcp" {
		r.Reason = "TURN control transport 必須為 udp 或 tcp"
		return
	}
	peer, err := resolveSyntheticPeer(ctx, probe.PeerAddress)
	if err != nil {
		r.Reason = "TURN 授權 STUN peer 無法解析"
		return
	}
	server, err := resolveSyntheticPeer(ctx, probe.Address)
	if err != nil {
		r.Reason = "TURN server 無法解析"
		return
	}
	var control net.PacketConn
	if probe.Network == "tcp" {
		conn, err := (&net.Dialer{Timeout: timeout(cfg)}).DialContext(ctx, "tcp", server.String())
		if err != nil {
			r.Status = Fail
			r.Reason = "TURN TCP control 無法連線"
			return r, cleanup
		}
		control = turn.NewSTUNConn(conn)
	} else {
		var err error
		control, err = (&net.ListenConfig{}).ListenPacket(ctx, "udp4", "0.0.0.0:0")
		if err != nil {
			r.Reason = "TURN UDP control 無法建立"
			return r, cleanup
		}
	}
	defer control.Close()
	client, err := turn.NewClient(&turn.ClientConfig{STUNServerAddr: server.String(), TURNServerAddr: server.String(), Conn: control, Username: credentials.Username, Password: credentials.Password, Realm: probe.Realm, RTO: 150 * time.Millisecond, LoggerFactory: silentTURNLogger()})
	if err != nil {
		r.Reason = "TURN server 設定或解析無效"
		return
	}
	defer client.Close()
	stopClose := context.AfterFunc(ctx, func() { client.Close(); _ = control.Close() })
	defer stopClose()
	if err := client.Listen(); err != nil {
		r.Status = Fail
		r.Reason = "TURN control 接收器無法啟動"
		return
	}
	relay, err := client.Allocate()
	if err != nil {
		r.Status = Fail
		r.Reason = "TURN 專用身分 allocation 失敗"
		return
	}
	defer func() {
		cleanup.Status = Pass
		cleanup.Reason = "已送出 allocation 釋放並關閉 TURN 本地連線"
		if err := relay.Close(); err != nil {
			cleanup.Status = Warn
			cleanup.Reason = "allocation 釋放未完成；本地連線已關閉"
		}
	}()
	stopRelayRead := context.AfterFunc(ctx, func() { _ = relay.SetReadDeadline(time.Now()) })
	defer stopRelayRead()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	if err := relay.SetDeadline(deadline); err != nil {
		r.Reason = "TURN relay 無法配置逾時"
		return
	}
	request, err := stun.Build(stun.TransactionID, stun.BindingRequest)
	if err != nil {
		r.Reason = "TURN relay 測試訊息無法建立"
		return
	}
	if _, err := relay.WriteTo(request.Raw, peer); err != nil {
		r.Status = Fail
		r.Reason = "TURN relay permission 或轉送失敗"
		return
	}
	buffer := make([]byte, 2048)
	for {
		n, source, err := relay.ReadFrom(buffer)
		if err != nil {
			r.Status = Fail
			r.Reason = "TURN relayed STUN 往返失敗或逾時"
			return r, cleanup
		}
		if source.String() != peer.String() {
			continue
		}
		message := &stun.Message{Raw: append([]byte(nil), buffer[:n]...)}
		if message.Decode() != nil || message.TransactionID != request.TransactionID || message.Type != stun.BindingSuccess {
			continue
		}
		r.Status = Pass
		r.Reason = "TURN allocation 與授權 peer 的實際 relayed packet 往返成功"
		return
	}
}

func resolveSyntheticPeer(ctx context.Context, address string) (*net.UDPAddr, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	portNumber, err := net.DefaultResolver.LookupPort(ctx, "udp", port)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("peer unavailable")
	}
	for _, ip := range ips {
		if v4 := ip.IP.To4(); v4 != nil {
			return &net.UDPAddr{IP: v4, Port: portNumber}, nil
		}
	}
	return nil, errors.New("IPv4 TURN peer required")
}
