package loadtest

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
)

const (
	ClassAuth        = "auth"
	ClassTimeout     = "timeout"
	ClassHTTP        = "http"
	ClassConflict    = "conflict"
	ClassGone        = "gone"
	ClassMalformed   = "malformed_json"
	ClassWebRTCSetup = "webrtc_setup"
	ClassWebRTCMedia = "webrtc_media"
	ClassNetwork     = "network"
	ClassConfig      = "config"
	ClassCancelled   = "cancelled"
	ClassUnknown     = "unknown"
)

func ClassifyError(status int, body []byte, err error) string {
	if errors.Is(err, context.Canceled) {
		return ClassCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ClassTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ClassTimeout
	}
	switch status {
	case 400:
		if looksMalformed(body) {
			return ClassMalformed
		}
		return ClassHTTP
	case 401, 403:
		return ClassAuth
	case 408:
		return ClassTimeout
	case 409:
		return ClassConflict
	case 410:
		return ClassGone
	case 500:
		return ClassHTTP
	}
	if status > 0 {
		return ClassHTTP
	}
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "webrtc") {
			return ClassWebRTCSetup
		}
		return ClassNetwork
	}
	return ClassUnknown
}

func looksMalformed(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var decoded any
	if json.Unmarshal(body, &decoded) != nil {
		return true
	}
	return false
}
