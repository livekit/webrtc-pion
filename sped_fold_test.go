// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package webrtc

import (
	"sync"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v4/test"
	"github.com/pion/transport/v4/vnet"
	"github.com/stretchr/testify/assert"
)

// spedConnectGap connects an offerer/answerer pair over a vnet with a fixed
// one-way link delay and returns the interval between the offerer's ICE
// transport reaching Connected and its PeerConnection reaching Connected. That
// interval is the DTLS handshake cost that is NOT overlapped with ICE:
//
//   - folded (SPED, non-blocking StartDial/StartAccept): the DTLS ClientHello is
//     piggybacked into STUN during ICE, so DTLS is done ~when ICE connects and
//     the gap is well under one RTT.
//   - not folded (blocking Dial/Accept, or SPED disabled): DTLS only begins once
//     ICE is connected, adding roughly two RTT to the gap.
func spedConnectGap(t *testing.T, enableSped bool, oneWayDelay time.Duration) time.Duration {
	t.Helper()

	wan, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "1.2.3.0/24",
		MinDelay:      oneWayDelay,
		LoggerFactory: logging.NewDefaultLoggerFactory(),
	})
	assert.NoError(t, err)

	newPC := func(ip string) *PeerConnection {
		vn, netErr := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{ip}})
		assert.NoError(t, netErr)
		assert.NoError(t, wan.AddNet(vn))

		se := SettingEngine{}
		se.SetNet(vn)
		se.EnableSped(enableSped)
		// Generous timeouts so the injected delay never trips ICE failure.
		se.SetICETimeouts(5*time.Second, 5*time.Second, 500*time.Millisecond)

		pc, pcErr := NewAPI(WithSettingEngine(se)).NewPeerConnection(Configuration{})
		assert.NoError(t, pcErr)

		return pc
	}

	offer := newPC("1.2.3.4")
	answer := newPC("1.2.3.5")
	assert.NoError(t, wan.Start())

	var mu sync.Mutex
	var iceAt, pcAt time.Time
	done := make(chan struct{})

	offer.OnICEConnectionStateChange(func(s ICEConnectionState) {
		if s == ICEConnectionStateConnected {
			mu.Lock()
			if iceAt.IsZero() {
				iceAt = time.Now()
			}
			mu.Unlock()
		}
	})
	offer.OnConnectionStateChange(func(s PeerConnectionState) {
		if s == PeerConnectionStateConnected {
			mu.Lock()
			if pcAt.IsZero() {
				pcAt = time.Now()
				close(done)
			}
			mu.Unlock()
		}
	})

	assert.NoError(t, signalPair(offer, answer))
	<-done

	mu.Lock()
	gap := pcAt.Sub(iceAt)
	mu.Unlock()

	closePairNow(t, offer, answer)
	assert.NoError(t, wan.Stop())

	return gap
}

// TestSpedFoldsDTLSIntoICE is a regression guard for the WARP/SPED "fold": when
// SPED is enabled the DTLS handshake must be piggybacked into ICE (DTLS-in-STUN)
// so it overlaps connectivity checks, rather than running as a separate handshake
// after ICE connects.
//
// A plain "does it connect?" test (see TestWarp) does NOT catch a broken fold —
// the connection still succeeds, it just silently falls back to post-ICE DTLS and
// loses the 1-2 RTT saving. This test fails deterministically if, e.g., a rebase
// reverts ICETransport.Start from the non-blocking agent.StartDial/StartAccept to
// the blocking agent.Dial/Accept (upstream #3371), which re-serializes DTLS after
// ICE.
func TestSpedFoldsDTLSIntoICE(t *testing.T) {
	lim := test.TimeOut(30 * time.Second)
	defer lim.Stop()

	const oneWayDelay = 50 * time.Millisecond // RTT ~100ms
	rtt := 2 * oneWayDelay

	// SPED off is the control: DTLS runs after ICE, so the gap is ~2 RTT.
	gapOff := spedConnectGap(t, false, oneWayDelay)
	// SPED on must fold DTLS into ICE, so the gap is a small fraction of a RTT.
	gapOn := spedConnectGap(t, true, oneWayDelay)

	t.Logf("ICE-connected -> PC-connected gap: sped-off=%v sped-on=%v (rtt=%v)", gapOff, gapOn, rtt)

	// Folded DTLS must land within one RTT of ICE (it is nearly done already);
	// unfolded DTLS needs ~2 RTT. One RTT is a comfortable separator.
	assert.Less(t, gapOn, rtt,
		"SPED enabled but DTLS did not fold into ICE (gap ~%v >= one RTT). Check "+
			"ICETransport.Start uses non-blocking agent.StartDial/StartAccept, not "+
			"the blocking Dial/Accept.", gapOn)

	// And the fold must be a clear win over the unfolded control.
	assert.Less(t, gapOn+oneWayDelay, gapOff,
		"SPED gap (%v) not meaningfully smaller than non-SPED gap (%v): DTLS is not "+
			"overlapping ICE", gapOn, gapOff)
}
