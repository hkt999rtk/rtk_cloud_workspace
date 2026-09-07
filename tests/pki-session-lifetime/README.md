# Go owner-to-media lifetime regression

Run from this directory:

```sh
GOWORK=off go test -race ./... -count=1
```

Local replacements compose the checked-out Cloud Client and Ameba WebRTC Go SDKs
under their distinct canonical module paths. The test creates a real local
WebSocket owner, passes its authorization context to the pure-Go device peer,
negotiates ICE/DTLS/SRTP with a local Pion viewer and receives H.264 RTP. Remote
owner closure, unread-message overflow and parent cancellation must tear down
the device peer and reject subsequent media samples.

The authority decision is a test trigger. Registry/certificate/token verification
has separate Video Cloud tests; this test does not qualify a deployed authority,
TURN service, or production C/firmware device. The transport package separately
checks heartbeat failure against an unresponsive peer without waiting for the
production heartbeat interval.
