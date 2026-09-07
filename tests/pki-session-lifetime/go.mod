module rtk-cloud-workspace/tests/pki-session-lifetime

go 1.25.13

require (
	github.com/hkt999rtk/rtk_ameba_webrtc/packages/golang v0.0.0
	github.com/hkt999rtk/rtk_cloud_client/packages/golang v0.0.0
	github.com/pion/webrtc/v4 v4.2.3-securityfix
	nhooyr.io/websocket v1.8.11
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/pion/datachannel v1.6.0 // indirect
	github.com/pion/dtls/v3 v3.1.4 // indirect
	github.com/pion/ice/v4 v4.2.0 // indirect
	github.com/pion/interceptor v0.1.43 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.1.0 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.16 // indirect
	github.com/pion/rtp v1.10.1 // indirect
	github.com/pion/sctp v1.9.2 // indirect
	github.com/pion/sdp/v3 v3.0.17 // indirect
	github.com/pion/srtp/v3 v3.0.10 // indirect
	github.com/pion/stun/v3 v3.1.5 // indirect
	github.com/pion/transport/v4 v4.0.2 // indirect
	github.com/pion/turn/v4 v4.1.4 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/time v0.14.0 // indirect
)

replace github.com/hkt999rtk/rtk_cloud_client/packages/golang => ../../repos/rtk_cloud_client/packages/golang

replace github.com/hkt999rtk/rtk_ameba_webrtc/packages/golang => ../../repos/rtk_ameba_webrtc/packages/golang
