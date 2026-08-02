#!/bin/sh
set -eu

agent_repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
server_repo=${KOMARI_REPO:-"$agent_repo/../komari"}

test -f "$server_repo/contracts/rpc-v2.json"
diff -w -B "$agent_repo/protocol/telemetryv2/testdata/report_v2.hex" \
  "$server_repo/protocol/telemetryv2/testdata/report_v2.hex"
diff -w -B "$agent_repo/protocol/telemetryv3/testdata/report_v3.hex" \
  "$server_repo/protocol/telemetryv3/testdata/report_v3.hex"
diff -w -B "$agent_repo/protocol/telemetryv3/SCHEMA.md" \
  "$server_repo/protocol/telemetryv3/SCHEMA.md"

node -e '
const fs = require("fs");
const contract = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const required = {
  "telemetry.v3": "3",
  "ping.leases": "1",
  "ping.result-batch": "1"
};
for (const [name, version] of Object.entries(required)) {
  if (contract.capabilities?.[name] !== version) {
    throw new Error(`missing capability ${name}@${version}`);
  }
}
if (contract.contract !== "komari.rpc.v2.4") {
  throw new Error(`unexpected contract ${contract.contract}`);
}
' "$server_repo/contracts/rpc-v2.json"

(cd "$agent_repo" && go test ./protocol/telemetryv2 ./protocol/telemetryv3 ./server)
(cd "$server_repo" && go test ./protocol/telemetryv2 ./protocol/telemetryv3 ./api/client ./api/jsonRpc)

echo "telemetry v2/v3, ACK and Ping lease contracts are compatible"
