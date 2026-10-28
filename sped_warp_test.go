// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package webrtc

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/stretchr/testify/assert"
)

// requireSpedActive proves the DTLS handshake was carried over STUN rather than
// falling back to plain DTLS. The ICE agent's piggybacking controller only
// leaves its Off/Tentative states once an embedded DTLS flight is actually
// queued, so an active (Confirmed/Pending/Complete) state means our
// DetachedConn→Piggyback path ran. The state field is unexported, hence reflect.
func requireSpedActive(t *testing.T, pc *PeerConnection) {
	t.Helper()
	agent := pc.dtlsTransport.iceTransport.gatherer.getAgent()
	assert.NotNil(t, agent)
	state := reflect.ValueOf(agent).Elem().FieldByName("piggyback").FieldByName("state").Int()
	switch state {
	case int64(ice.PiggybackingStateConfirmed),
		int64(ice.PiggybackingStatePending),
		int64(ice.PiggybackingStateComplete):
	default:
		t.Fatalf("SPED inactive: piggybacking state %d (Off/Tentative) — handshake fell back to plain DTLS", state)
	}
}

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
	<-peerConnectionsConnected

	requireSpedActive(t, offer)
	requireSpedActive(t, answer)

	closePairNow(t, offer, answer)
}

// TestSpedPiggybackReportsUnconsumed guards against the DTLS-v4-port regression
// where spedPiggyback discarded the bool returned by ICETransport.Piggyback and
// always reported the flight as consumed. The agent returns false when it will
// not carry a flight on STUN (no agent yet, or SPED off/complete once ICE is
// connected); the flight must then be written as plain DTLS. If that signal is
// swallowed the flight is dropped and the handshake can stall. The pre-v4
// interceptor returned the value directly (livekit/dtls writeHandshakePackets
// wrote the un-consumed packet on the wire); this keeps parity.
func TestSpedPiggybackReportsUnconsumed(t *testing.T) {
	s := SettingEngine{}
	s.EnableSped(true)
	api := NewAPI(WithSettingEngine(s))

	pc, err := api.NewPeerConnection(Configuration{})
	assert.NoError(t, err)
	defer func() { assert.NoError(t, pc.Close()) }()

	piggyback := pc.dtlsTransport.spedPiggyback()
	assert.NotNil(t, piggyback, "spedPiggyback must be set when SPED is enabled")

	// No ICE agent exists before gathering, so the agent cannot carry the
	// flight: Piggyback returns false and the flight must be reported as not
	// consumed, so the caller falls back to a plain-DTLS socket write.
	handled, err := piggyback([][]byte{{0x16, 0x00, 0x01}})
	assert.NoError(t, err)
	assert.False(t, handled, "a declined flight must be reported unconsumed so it falls back to plain DTLS")
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
	<-peerConnectionsConnected

	requireSpedActive(t, offer)
	requireSpedActive(t, answer)

	closePairNow(t, offer, answer)
}
