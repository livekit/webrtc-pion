// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package webrtc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWarp(t *testing.T) {
	s := SettingEngine{}
	s.EnableSped(true)
	api := NewAPI(WithSettingEngine(s))

	offer, err := api.NewPeerConnection(Configuration{})
	assert.NoError(t, err)
	answer, err := api.NewPeerConnection(Configuration{})
	assert.NoError(t, err)

	peerConnectionsConnected := untilConnectionState(PeerConnectionStateConnected, offer, answer)
	assert.NoError(t, signalPair(offer, answer))
	peerConnectionsConnected.Wait()

	closePairNow(t, offer, answer)
}

func TestWarpClient(t *testing.T) {
	s := SettingEngine{}
	s.EnableSped(true)
	api := NewAPI(WithSettingEngine(s))

	offer, err := api.NewPeerConnection(Configuration{})
	assert.NoError(t, err)
	answer, err := api.NewPeerConnection(Configuration{})
	assert.NoError(t, err)

	peerConnectionsConnected := untilConnectionState(PeerConnectionStateConnected, offer, answer)
	assert.NoError(t, signalPairWithModification(
		offer, answer,
		func(sessionDescription string) string {
			return strings.ReplaceAll(
				sessionDescription,
				"setup:actpass",
				"setup:active")
		}))
	peerConnectionsConnected.Wait()

	closePairNow(t, offer, answer)
}
