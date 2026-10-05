// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package webrtc

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestForwardDTLSPacketDoesNotBlockOnTransportLock guards the SPED deadlock fix:
// forwardDTLSPacket runs on the ICE agent task loop and must take only
// dtlsCbLock, never t.lock. Renegotiation paths (e.g. setRemoteCredentials)
// hold t.lock while synchronously blocking on that loop, so if forwardDTLSPacket
// took t.lock the two would deadlock. Here we hold t.lock (standing in for a
// renegotiation) and require forwardDTLSPacket to still complete.
func TestForwardDTLSPacketDoesNotBlockOnTransportLock(t *testing.T) {
	it := &ICETransport{}

	it.lock.Lock()
	defer it.lock.Unlock()

	done := make(chan struct{})
	go func() {
		// dtlsCallback is nil, so this takes the buffer path (dtlsCbLock only).
		it.forwardDTLSPacket([]byte{0x16, 0x00, 0x01}, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("forwardDTLSPacket blocked on t.lock — deadlock risk with renegotiation")
	}
}

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
