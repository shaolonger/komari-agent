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
