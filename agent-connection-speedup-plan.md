# Agent Connection Speed-up Plan — WARP + Prewarm

How an agent/client connection is established today, where the round-trips go, and two
optimizations (**WARP** and **Prewarm connection pool**) that remove most of them.

All measurements: **controlled 100 ms RTT**, single PeerConnection, pion/livekit-server with
`enable_warp`, rust `sped_bench` / agent harness. p50 unless noted.

---

## 1. Current connection stages

A cold connect walks these stages **sequentially**. Each network exchange is ~1 RTT (~100 ms here).

| # | Stage | What happens | RTT (cold) | ~ms @100ms |
|---|---|---|---|---|
| 1 | **TCP** | SYN / SYN-ACK to the signaling host | 1 | ~100 |
| 2 | **TLS** (`wss` only) | TLS 1.3 ClientHello … Finished | 1 | ~100 |
| 3 | **WS upgrade + signaling join** | the join request rides *in* the WS upgrade (`GET …?join_request=…`); server returns `101` + `JoinResponse` (+ SDP answer) in the **same round-trip** — upgrade and join are overlapped, not separate hops | 1 | ~100 |
| 4 | **ICE** | candidate gather + connectivity check on the selected pair | 1 (+~50 ms pacing) | ~110 |
| 5 | **DTLS** | mutual-cert handshake (boringssl client), 4–6 flights | 2 | ~200 |
| 6 | **SCTP** | data-channel association: INIT / INIT-ACK / COOKIE-ECHO / COOKIE-ACK | 2 | ~200 |

Measured phase grouping (`sped_bench`, `ws`, what the SDK actually reports):

| Phase | stages | baseline @100ms |
|---|---|---|
| **A. Signaling** | TCP (+TLS) + WS-upgrade/join (overlapped) | ~216 ms (2 RTT `ws`; +1 RTT TLS for `wss`) |
| **B. ICE** | gather + connectivity check | ~114 ms (1 RTT, after disabling the loopback candidate) |
| **C. DTLS** | handshake until PeerConnection `connected` | ~250 ms (~2.5 RTT) |
| **D. SCTP / data channel** | after PC connected (publish/data) | ~1–2 RTT |

**Total cold agent connect (no optimization): ~582 ms ≈ 5.8 RTT** (`ws`); ~680 ms for `wss`.
DTLS and SCTP each run *after* the preceding stage, so they're additive — that's the target.

---

## 2. What we can do to speed it up

Two orthogonal optimizations (compose; ship both):

| Method | Mechanism | Removes | Expected @100ms | Server dep? |
|---|---|---|---|---|
| **WARP = SPED + SNAP** | **SPED**: embed DTLS flights inside ICE STUN binding req/resp → DTLS completes *during* ICE instead of after. **SNAP**: carry the SCTP INIT in SDP → data channel ready without the on-wire INIT exchange. | DTLS phase (~2 RTT), SCTP phase (~2 RTT) | **−1 to −1.5 RTT** on connect; data channel ~immediate | **Yes** — needs WARP-capable SFU + SPED libwebrtc |
| **Prewarm pool** | Pre-establish TCP (+TLS) to the signaling host *off the critical path* (while idle); `connect()` reuses the warm socket and only does the WS upgrade. | TCP (`ws`) or TCP+TLS (`wss`) from stage 1–2 | **−1 RTT `ws`, −2 RTT `wss`** | **No** — pure client-side |

> **Ship SPED and SNAP together.** SPED alone regressed the post-connect publish/data-channel
> path (intermittent +400–600 ms); enabling SNAP removed it.

---

## 3. WARP — benchmark, RTT elimination, landing plan

### How it eliminates RTTs
- **SPED (DTLS-in-STUN):** normally DTLS runs *after* ICE connects (sequential, ~2 RTT). SPED
  piggybacks the DTLS flights in the ICE STUN binding request/response, so the handshake finishes
  *concurrently with* ICE — the standalone DTLS phase collapses from ~2 RTT to ~0.
- **SNAP (SCTP-INIT-in-SDP):** the SCTP INIT parameters travel in the SDP, so the data-channel
  association is ready right after DTLS instead of paying the 4-way INIT (~2 RTT).

### Benchmark (100 ms RTT, agent connect, worker-measured ICE+DTLS)
| config | connect | DTLS phase |
|---|---|---|
| baseline | 582 ms | ~250 ms (~2.5 RTT) |
| SPED (+ pool) | 335 ms | ~103 ms (~1 RTT, folded into ICE) |

SPED contributes **~−1.5 RTT** of the connect saving. Verified negotiated end-to-end via
`DtlsTransport … dtls_in_stun: 1`. (Confirmed on LiveKit Cloud too — it negotiates SPED — though
WAN path noise there was too large to isolate the ~1 RTT gain.)

### Plan to land in prod
1. **pion forks** (`webrtc/v4`, `dtls/v3`, `ice/v4`, `stun`, `sctp`) — SPED/SNAP wire protocol; upstream / pin.
2. **libwebrtc** — ship a SPED-bearing build (`goog-sped-v1` is in upstream `main`); resolve the fork delta.
3. **livekit-server** — `enable_warp` config (SPED+SNAP on the SFU), behind a flag.
4. Client enables field trials: `WebRTC-IceHandshakeDtls/Enabled/WebRTC-Sctp-Snap/Enabled/`.

### TODOs
- [ ] Productionize the SPED libwebrtc build in CI (currently a local custom build for the rust-sdk).
- [ ] Roll out `enable_warp` on the SFU behind a flag; verify `dtls_in_stun:1` in staging.
- [ ] **Keep the default DTLS role** (SFU = DTLS client / `setup:active`); the flipped role regressed.
- [ ] Always enable **SPED + SNAP together** (SPED-alone regresses the data-channel path).
- [ ] Re-measure on a clean WAN path (low-loss, close region, no VPN) to confirm the ~1 RTT gain.

---

## 4. Prewarm connection — benchmark, RTT elimination, API & requirements

### How it eliminates TCP/TLS RTTs
A cold `connect()` pays TCP (stage 1) and, for `wss`, TLS (stage 2) on the critical path. The
prewarm pool dials TCP(+TLS) **ahead of time while idle** and stashes the connected stream keyed by
`scheme://host:port`. The next `connect()` to that endpoint pops the warm stream and runs **only**
the WebSocket upgrade — TCP/TLS are already paid, off the clock.

### Benchmark
**Controlled 100 ms RTT, `ws`** (`sped_bench`, single-PC):
| | signal phase | total |
|---|---|---|
| prewarm OFF | ~216 ms (2 RTT) | 440 ms |
| prewarm ON | ~113 ms (1 RTT) | 336 ms — **−104 ms (−1 RTT, the TCP handshake)** |

**Cloud `wss`** (real TLS, noisy WAN, 2 runs): signaling **~730–800 ms → ~270 ms (−~500 ms)**,
total p50 **~2000 → ~1370 ms**. Prewarm is the dominant, reproducible WAN win (saves TCP **and** TLS).

### API changes (status: implemented except where noted)
| layer | change | status |
|---|---|---|
| `livekit-api` (rust) | `prewarm_connect(url)` + take-once `host:port` registry in `signal_client/signal_stream.rs`; `connect_inner` reuses a warm stream via the existing `client_async_with_config` path. Re-exported from `livekit` crate (`tokio` feature). | ✅ |
| `livekit-ffi` | `PrewarmRequest`/`PrewarmResponse`/`PrewarmCallback` in `room.proto` + `ffi.proto` oneofs; `on_prewarm` handler (spawns `prewarm_connect`, emits callback). | ✅ |
| `python-sdks/livekit-rtc` | `rtc.prewarm(url)` + regenerated `_proto`. | ✅ |
| app / agents | warm-pool policy (how many, refresh interval) lives in **application code**, not the SDK. | app-side |

### Requirements / notes
- **Pooling policy stays out of the SDK** — the SDK only exposes the per-connection `prewarm` primitive; the pool (depth, refresh, eviction) is the caller's.
- Warm sockets are **pre-upgrade** (TCP/TLS only) — they're reusable for any room, but are subject to the server's idle/keepalive timeout, so the pool must refresh.
- The agent worker must know the **server URL** at warm time (true at worker startup) and warm on idle (e.g. `num_idle_processes` runners / a background top-up task).
- Avoid TLS 1.3 **0-RTT early data** for the join (replay risk).
- **Biggest residual win (optional): publish-on-connect.** Publishing a track currently triggers a post-connect renegotiation (~106 ms / ~1 RTT). Adding a tracks-on-join argument to `ConnectRequest` (rust `RoomOptions` → FFI → python) would fold it into the initial offer. *Not currently exposed — todo.*

---

## End-to-end result (realistic ordering: user already in room, then agent dispatched)

| config | agent_join (dispatch → user sees agent) |
|---|---|
| baseline | **1206 ms** |
| WARP (SPED+SNAP) + prewarm | **958 ms** (−248 ms, ~21%) |

The −248 ms equals the connect saving — it passes straight through. The remaining ~700 ms is fixed,
non-WARP overhead: dispatch control-plane (~407 ms ≈ 4 RTT), publish renegotiation (~106 ms), and
participant announce (~111 ms). The dispatch control-plane is the next-largest lever, separate from WARP.
