#!/usr/bin/env bash
set -uo pipefail

mkdir -p /logs/verifier
reward=0
write_reward() {
  printf '%s\n' "$reward" > /logs/verifier/reward.txt
}
trap write_reward EXIT

GO_BIN=/usr/local/go/bin/go
if [ "$(id -u)" -eq 0 ] || [ "$(id -un)" != "verifier" ]; then
  echo "verifier must run as the dedicated non-root verifier user" >&2
  exit 1
fi
if [ ! -x "$GO_BIN" ] || [ "$(stat -c '%U' "$GO_BIN")" != "root" ]; then
  echo "trusted Go binary is missing or is not root-owned" >&2
  exit 1
fi
go_mode=$(stat -c '%a' "$GO_BIN")
if [ $((8#$go_mode & 022)) -ne 0 ]; then
  echo "trusted Go binary is group/world-writable" >&2
  exit 1
fi
if [ "$(stat -c '%U' /logs/verifier)" != "verifier" ]; then
  echo "verifier reward directory is not verifier-owned" >&2
  exit 1
fi
logs_mode=$(stat -c '%a' /logs/verifier)
if [ $((8#$logs_mode & 022)) -ne 0 ]; then
  echo "verifier reward directory is group/world-writable" >&2
  exit 1
fi

install -m 0644 /tests/oras_copy_recovery_test.go /app/oras_copy_recovery_test.go || exit 1
install -m 0644 /tests/syncutil_recovery_test.go /app/internal/syncutil/talents_recovery_test.go || exit 1
install -m 0644 /tests/graph_digest_identity_test.go /app/internal/graph/talents_digest_identity_test.go || exit 1
install -m 0644 /tests/content_reader_recovery_test.go /app/content/talents_reader_recovery_test.go || exit 1
install -m 0644 /tests/bad_digest_stores_test.go /app/content/talents_bad_digest_stores_test.go || exit 1
install -m 0644 /tests/file_restore_recovery_test.go /app/content/file/talents_restore_recovery_test.go || exit 1
install -m 0644 /tests/remote_bad_digest_test.go /app/registry/remote/talents_bad_digest_test.go || exit 1
install -m 0644 /tests/credential_ingest_atomicity_test.go /app/registry/remote/credentials/internal/ioutil/talents_ingest_atomicity_test.go || exit 1
install -m 0644 /tests/oci_ingest_atomicity_test.go /app/content/oci/talents_ingest_atomicity_test.go || exit 1
install -m 0644 /tests/pack_config_validation_test.go /app/talents_pack_config_validation_test.go || exit 1
install -m 0644 /tests/manifest_fetch_length_test.go /app/registry/remote/talents_manifest_fetch_length_test.go || exit 1
install -m 0644 /tests/repository_pagination_bound_test.go /app/registry/remote/talents_repository_pagination_bound_test.go || exit 1
install -m 0644 /tests/proxy_singleflight_test.go /app/internal/cas/talents_proxy_singleflight_test.go || exit 1
install -m 0644 /tests/auth_origin_boundary_test.go /app/registry/remote/auth/talents_auth_origin_boundary_test.go || exit 1

cd /app || exit 1
export GOTOOLCHAIN=local
export GOMAXPROCS=4

"$GO_BIN" test -count=1 -timeout=360s ./... || exit 1
"$GO_BIN" test -race -count=3 -timeout=300s ./internal/syncutil ./internal/graph ./content/file ./internal/cas ./registry/remote/auth || exit 1

reward=1
