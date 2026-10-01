#!/bin/sh
# Disposable container test: never touches the normal forum-data volume.
set -eu
cd "$(dirname "$0")/.."
image="discuss-hub:audit"
container="discuss-hub-audit-$$"
volume="discuss-hub-audit-data-$$"
cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM

docker image build -t "$image" .
docker volume create "$volume" >/dev/null
docker run -d --name "$container" --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true \
  -p 127.0.0.1::8080 -v "$volume:/app/data" "$image" -demo >/dev/null
port=$(docker port "$container" 8080/tcp | sed 's/.*://')
base="http://127.0.0.1:$port"
wait_ready() {
  attempt=0
  until curl --fail --silent "$base/healthz" >/dev/null; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 40 ]; then
      docker logs "$container"
      exit 1
    fi
    sleep 1
  done
}
wait_ready
curl --fail --silent "$base/" | grep -q 'What daily practice teaches us'
curl --fail --silent "$base/insights" | grep -q 'Overall forum mood'
curl --fail --silent "$base/insights/trending?format=json" | grep -q '"topics"'
[ "$(docker exec "$container" id -u)" = 10001 ]
# Restart the same persisted data without re-running the one-time seed.
docker stop --time 15 "$container" >/dev/null
docker rm "$container" >/dev/null
docker run -d --name "$container" --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true \
  -p "127.0.0.1:$port:8080" -v "$volume:/app/data" "$image" >/dev/null
wait_ready
curl --fail --silent "$base/posts/1" | grep -q 'What daily practice teaches us'
curl --fail --silent "$base/insights" | grep -q 'demo_practice'
printf '%s\n' 'PASS: image build, non-root user, HTTP pages, writable volume, graceful stop, persisted restart.'
