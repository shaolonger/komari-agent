# Komari Telemetry Protocol v2

- WebSocket subprotocol: `komari.telemetry.v2`
- Legacy fallback subprotocol: `komari.telemetry.v1`
- Byte order: little-endian
Maximum frame: 65,536 bytes

## Header（16 bytes）

| Offset | Type | Meaning |
|---:|---|---|
| 0 | `[4]byte` | Magic `KMR2` |
| 4 | `uint8` | Version `2` |
| 5 | `uint8` | Flags: bit 0 GPU present, bit 1 detailed GPU |
| 6 | `uint16` | Header size `16` |
| 8 | `uint32` | Exact payload byte length |
| 12 | `uint32` | Schema ID `0x32525054` |

Unknown versions, flags, schema IDs, trailing bytes and inconsistent lengths are rejected.

## Core payload

Fields are encoded in this exact order:

1. CPU usage `float64`;
2. RAM total/used `uint64 × 2`;
3. Swap total/used `uint64 × 2`;
4. load1/load5/load15 `float64 × 3`;
5. Disk total/used `uint64 × 2`;
6. Network up/down/totalUp/totalDown `uint64 × 4`;
7. TCP/UDP counts `uint32 × 2`;
8. Uptime `uint64`;
9. Process count `uint32`;
10. Message as `uint16 byteLength + UTF-8 bytes`（maximum 4,096 bytes）.

## Optional GPU payload

When GPU is present, a `uint16` item count follows. The maximum is 64.

- Detailed mode: average usage `float64`, then each device as name string, memory total `uint64`, memory used `uint64`, utilization `float64`, temperature `uint64`.
- Fallback mode: each item is one model-name string.

GPU names are valid UTF-8 with a 256-byte maximum. Encoders and decoders reject non-finite floats, invalid counts, `used > total`, truncation and oversized input. Failed v2 encoding must send the existing JSON v1 text frame instead.
