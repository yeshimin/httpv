#!/bin/sh
set -eu

DIRECT_URL=${DIRECT_URL:-http://127.0.0.1:9998/}
GATEWAY_URL=${GATEWAY_URL:-http://127.0.0.1:8088/}
DURATION=${DURATION:-30s}
CONCURRENCY=${CONCURRENCY:-128}
RATE=${RATE:-1000}
LABEL=${LABEL:-baseline}

bench_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
result_dir="$bench_dir/results/$timestamp-$LABEL"
mkdir -p "$result_dir"

{
  uname -a
  go version
  printf 'duration=%s concurrency=%s rate=%s\n' "$DURATION" "$CONCURRENCY" "$RATE"
} > "$result_dir/environment.txt"

(
  cd "$bench_dir/loadgen"
  go run . -url "$DIRECT_URL" -duration "$DURATION" -concurrency "$CONCURRENCY" -rate "$RATE" -json
) | tee "$result_dir/direct.json"

(
  cd "$bench_dir/loadgen"
  go run . -url "$GATEWAY_URL" -duration "$DURATION" -concurrency "$CONCURRENCY" -rate "$RATE" -json
) | tee "$result_dir/gateway.json"

printf 'results=%s\n' "$result_dir"
