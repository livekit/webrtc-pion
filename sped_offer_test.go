// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package webrtc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSpedOfferAdvertisesGoogSpedV1 guards the downstream (server-offers) path:
// when SPED is enabled the offer MUST carry `a=ice-options:trickle goog-sped-v1`,
// otherwise a libwebrtc answerer will not enable DTLS-in-STUN and the subscriber
// PC silently falls back to plain DTLS (no fold). The "trickle" token must be
// present too, or a pion peer treats us as non-trickle and de-paces ICE.
func TestSpedOfferAdvertisesGoogSpedV1(t *testing.T) {
	s := SettingEngine{}
	s.EnableSped(true)
	pc, err := NewAPI(WithSettingEngine(s)).NewPeerConnection(Configuration{})
	assert.NoError(t, err)
	defer func() { _ = pc.Close() }()

	_, err = pc.AddTransceiverFromKind(RTPCodecTypeAudio, RTPTransceiverInit{
		Direction: RTPTransceiverDirectionSendonly,
	})
	assert.NoError(t, err)

	offer, err := pc.CreateOffer(nil)
	assert.NoError(t, err)

	var iceOptions string
	for _, line := range strings.Split(offer.SDP, "\r\n") {
		if strings.HasPrefix(line, "a=ice-options:") {
			iceOptions = line
			break
		}
	}
	assert.NotEmpty(t, iceOptions, "SPED offer must contain an a=ice-options line")
	assert.Contains(t, iceOptions, "goog-sped-v1",
		"SPED offer must advertise goog-sped-v1 so a libwebrtc answerer enables the fold")
	assert.Contains(t, iceOptions, "trickle",
		"ice-options must keep trickle or a pion peer de-paces ICE and the fold breaks")
}

// TestNoSpedNoGoogSpedV1 confirms the option is opt-in: with SPED disabled the
// offer must not advertise goog-sped-v1.
func TestNoSpedNoGoogSpedV1(t *testing.T) {
	pc, err := NewAPI().NewPeerConnection(Configuration{})
	assert.NoError(t, err)
	defer func() { _ = pc.Close() }()

	_, err = pc.AddTransceiverFromKind(RTPCodecTypeAudio, RTPTransceiverInit{
		Direction: RTPTransceiverDirectionSendonly,
	})
	assert.NoError(t, err)

	offer, err := pc.CreateOffer(nil)
	assert.NoError(t, err)
	assert.NotContains(t, offer.SDP, "goog-sped-v1")
}
