#!/usr/bin/env bash
set -uo pipefail

mkdir -p /logs/verifier
reward=0
write_reward() {
  printf '%s\n' "$reward" > /logs/verifier/reward.txt
}
trap write_reward EXIT

install -m 0644 /tests/oras_copy_recovery_test.go /app/oras_copy_recovery_test.go || exit 1
install -m 0644 /tests/syncutil_recovery_test.go /app/internal/syncutil/talents_recovery_test.go || exit 1
install -m 0644 /tests/graph_digest_identity_test.go /app/internal/graph/talents_digest_identity_test.go || exit 1
install -m 0644 /tests/content_reader_recovery_test.go /app/content/talents_reader_recovery_test.go || exit 1
install -m 0644 /tests/bad_digest_stores_test.go /app/content/talents_bad_digest_stores_test.go || exit 1
install -m 0644 /tests/file_restore_recovery_test.go /app/content/file/talents_restore_recovery_test.go || exit 1
install -m 0644 /tests/remote_bad_digest_test.go /app/registry/remote/talents_bad_digest_test.go || exit 1

cd /app || exit 1
export PATH="/usr/local/go/bin:$PATH"
export GOTOOLCHAIN=local
export GOMAXPROCS=4

go test -count=1 -timeout=360s ./... || exit 1
go test -race -count=3 -timeout=240s ./internal/syncutil ./content/file || exit 1

reward=1
