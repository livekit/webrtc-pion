// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package detacheddtls

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWriteDatagramsFallbackOnUnconsumed guards the SPED fallback contract that
// spedPiggyback relies on: while the handshake is in progress, a flight reported
// as consumed (handled=true) must not touch the socket, but a flight the agent
// declined (handled=false) must be written as plain DTLS rather than dropped.
func TestWriteDatagramsFallbackOnUnconsumed(t *testing.T) {
	var written [][]byte
	consumed := true
	c := &Conn{config: Config{
		WriteDatagram: func(b []byte) (int, error) {
			written = append(written, append([]byte(nil), b...))

			return len(b), nil
		},
		PiggybackDatagrams: func([][]byte) (bool, error) { return consumed, nil },
	}}
	// c.ready is false: the handshake is still in progress.

	consumed = true
	assert.NoError(t, c.writeDatagrams([][]byte{{1}, {2}}))
	assert.Empty(t, written, "a consumed flight must not be written to the socket")

	consumed = false
	assert.NoError(t, c.writeDatagrams([][]byte{{3}, {4}}))
	assert.Equal(t, [][]byte{{3}, {4}}, written, "a declined flight must fall back to plain DTLS")
}
