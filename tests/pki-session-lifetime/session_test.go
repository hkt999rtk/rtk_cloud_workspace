package lifetime_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	media "github.com/hkt999rtk/rtk_ameba_webrtc/packages/golang/rtkc"
	"github.com/hkt999rtk/rtk_cloud_client/packages/golang/rtkc/transport"
	pion "github.com/pion/webrtc/v4"
	"nhooyr.io/websocket"
)

// Real websocket + both SDKs + local ICE/DTLS/SRTP. The authority decision is a
// test trigger; live registry/IdP and production firmware are separate gates.
func TestOwnerLossStopsRealMedia(t *testing.T) {
	for _, cause := range []string{"remote-close", "backlog", "parent-cancel"} {
		t.Run(cause, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			trigger := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer device-fixture" {
					http.Error(w, "denied", 403)
					return
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				readerDone := make(chan struct{})
				go func() { defer close(readerDone); conn.Read(ctx) }()
				select {
				case <-trigger:
				case <-ctx.Done():
				}
				if cause == "backlog" {
					for n := 0; n < 100; n++ {
						if conn.Write(ctx, websocket.MessageText, []byte("command")) != nil {
							break
						}
					}
				}
				if cause == "backlog" {
					select {
					case <-readerDone:
					case <-ctx.Done():
					}
				}
				conn.CloseNow()
				<-readerDone
			}))
			defer server.Close()
			owner, err := transport.ConnectWebSocket(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), "device-fixture", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			settings := pion.SettingEngine{}
			settings.SetNAT1To1IPs([]string{"127.0.0.1"}, pion.ICECandidateTypeHost)
			viewer, err := pion.NewAPI(pion.WithSettingEngine(settings)).NewPeerConnection(pion.Configuration{})
			if err != nil {
				t.Fatal(err)
			}
			defer viewer.Close()
			if _, err = viewer.AddTransceiverFromKind(pion.RTPCodecTypeVideo, pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionRecvonly}); err != nil {
				t.Fatal(err)
			}
			received := make(chan struct{}, 1)
			viewer.OnTrack(func(track *pion.TrackRemote, _ *pion.RTPReceiver) {
				if _, _, err := track.ReadRTP(); err == nil {
					received <- struct{}{}
				}
			})
			offer, err := viewer.CreateOffer(nil)
			if err != nil {
				t.Fatal(err)
			}
			gathered := pion.GatheringCompletePromise(viewer)
			if err = viewer.SetLocalDescription(offer); err != nil {
				t.Fatal(err)
			}
			select {
			case <-gathered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			device, answer, err := media.NewDeviceSession(owner.Context(), media.SessionDescription{Type: "offer", SDP: viewer.LocalDescription().SDP}, nil, media.DeviceOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer device.Close()
			if err = viewer.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
				t.Fatal(err)
			}
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
		receiving:
			for {
				select {
				case <-received:
					break receiving
				case <-ticker.C:
					if err = device.WriteH264([]byte{0, 0, 0, 1, 0x65, 0x88, 0x84}, time.Second/30); err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("no real RTP received", ctx.Err())
				}
			}
			if cause == "parent-cancel" {
				cancel()
			} else {
				close(trigger)
			}
			select {
			case <-device.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("media survived owner loss")
			}
			if cause == "backlog" && !errors.Is(context.Cause(owner.Context()), transport.ErrMessageBacklog) {
				t.Fatal("backlog did not cause shutdown", context.Cause(owner.Context()))
			}
			if owner.Context().Err() == nil {
				t.Fatal("owner lifetime did not end")
			}
			if err = device.WriteH264([]byte{0, 0, 0, 1, 0x65}, time.Second/30); err == nil {
				t.Fatal("media write accepted after authorization loss")
			}
		})
	}
}
