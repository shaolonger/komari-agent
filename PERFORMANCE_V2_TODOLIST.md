# Komari Agent Performance V2 Todo

Every checked item requires focused tests, full Go tests, race coverage for its
concurrency surface and a dedicated commit.

## A2-0 Baseline

- [x] **A2-001 Freeze the V2 design and clean test baseline**
- [x] **A2-002 Add long-run RSS/CPU/network and disconnected-spool fixtures**

## A2-1 Telemetry

- [x] **A2-101 Implement strictly bounded telemetry v3 codec and goldens**
- [x] **A2-102 Add adaptive aggregate envelopes and periodic checkpoints**
- [x] **A2-103 Negotiate v3 with secure v2/v1 fallback**
- [x] **A2-104 Add durable ACK tracking and a bounded 0600 spool**

## A2-2 Ping

- [x] **A2-201 Implement revisioned, expiring leased Ping schedules**
- [x] **A2-202 Batch Ping results with sequence/ACK retry semantics**
- [x] **A2-203 Prove all existing SSRF/capability limits on leased execution**

## A2-3 Acceptance and release

- [ ] **A2-301 Cross-repository protocol and contract compatibility matrix**
- [ ] **A2-302 Linux/Windows/FreeBSD builds, race, fuzz and resource soak**
- [ ] **A2-303 Version, push, tag and publish the Agent release**
