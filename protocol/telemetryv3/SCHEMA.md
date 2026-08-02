# Komari telemetry v3 wire schema

V3 uses WebSocket subprotocol `komari.telemetry.v3`. Every binary frame is at
most 64 KiB and contains a 32-byte little-endian header, a fixed 68-byte
aggregate envelope and one validated telemetry-v2 latest report. The header
contains magic/version/flags/header length/payload length/schema, a positive
monotonic sequence and Unix milliseconds. Flag bit 0 marks a complete periodic
checkpoint; all other flag bits fail closed.

The envelope contains sample count, CPU min/max/sum, RAM-used min/max/sum and
network total-counter deltas. It preserves peaks and totals while allowing a
lower network cadence. Counts, finite floats, ordering, every nested length and
the v2 report are validated before allocation or use.

## Durable control messages

The server sends JSON control messages as WebSocket text frames. A cumulative
acknowledgement has the shape
`{"type":"telemetry_ack","through":N,"accepted_through":M}` where `N` is the
highest sequence durably committed with its history rows and `M >= N` is the
highest sequence accepted by the current server process. Older servers may omit
`accepted_through`; agents must treat it as informational and must delete only
frames through `N`.

An ACK is never a replay instruction. The server sends only positive,
per-connection advancing durable ACKs. The agent applies duplicate or stale
ACKs as no-ops without spool writes and never re-enqueues pending frames because
of an ACK.

Only `{"type":"telemetry_nack","expected":N}` requests replay. The agent
replaces queued v3 frames with its durable pending suffix beginning at `N` and
keeps unrelated reliable control results. Replay is canceled when that
WebSocket generation ends. These rules preserve at-least-once recovery without
an ACK/replay feedback loop.
