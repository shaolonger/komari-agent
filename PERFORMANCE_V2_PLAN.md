# Komari Agent Performance V2

Status: complete; v1.4.0 release candidate

The existing sampler runtime, immutable report snapshot, telemetry v2 codec,
bounded outbound queue and Ping target policy remain the foundation. V2 reduces
server/agent control traffic and provides end-to-end bounded recovery without
weakening the default-disabled remote capabilities.

## Telemetry v3

V3 is explicitly negotiated after v2 and carries a monotonic sequence, sample
time, present-field bitset and a bounded aggregate envelope. The Agent continues
sampling fast metrics locally while the send cadence can be reduced when no
realtime lease exists. An envelope retains latest/min/max/sum/count and counter
deltas, so lower network frequency does not hide spikes or lose traffic totals.
A complete checkpoint is sent periodically and after reconnect. Unknown v3
versions, flags, lengths and non-finite values fail closed; v2/v1 fallback is
unchanged.

## Durable bounded delivery

The server acknowledges durable sequence ranges. Unacknowledged telemetry and
Ping-result batches may use a size- and age-bounded local spool with mode 0600.
The spool contains no token and is removed only after acknowledgement. Corrupt,
oversized or stale entries are quarantined/dropped with aggregate diagnostics,
never retried forever.

## Leased Ping schedules

Instead of receiving one control command per due probe, the Agent receives a
revisioned schedule with a monotonic local phase and an expiry. Disconnect or
lease expiry stops the schedule. Every execution still passes the existing
capability, type, port, DNS pinning, restricted-address, concurrency, interval,
timeout and response-size checks. Results are sent in bounded batches.

## Resource goals

Linux without GPU targets 20 MiB RSS p95 and 0.5% average CPU at one-second
local sampling. No network failure, server stall or task storm may grow memory,
goroutines, open files or disk spool beyond configured hard limits.
