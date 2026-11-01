#!/usr/bin/env bash
set -euo pipefail

# Private single-node search. No host port is published.
SEARCH_CONTAINER="${SEARCH_CONTAINER:-pax-search}"
SEARCH_NETWORK="${SEARCH_NETWORK:-pax-search-private}"
SEARCH_VOLUME="${SEARCH_VOLUME:-pax-search-data}"
SEARCH_IMAGE="${SEARCH_IMAGE:-docker.elastic.co/elasticsearch/elasticsearch:9.5.5}"
MANAGER_CONTAINER="${MANAGER_CONTAINER:-pax-manager}"
SEARCH_READY_ATTEMPTS="${SEARCH_READY_ATTEMPTS:-60}"
SEARCH_READY_INTERVAL="${SEARCH_READY_INTERVAL:-2}"

attach_container() {
  local container="$1"
  if docker container inspect "${container}" >/dev/null 2>&1; then
    if ! docker network inspect "${SEARCH_NETWORK}" --format '{{range .Containers}}{{println .Name}}{{end}}' | grep -Fxq "${container}"; then
      docker network connect "${SEARCH_NETWORK}" "${container}"
    fi
  fi
}

validate_wait_settings() {
  if [[ ! "${SEARCH_READY_ATTEMPTS}" =~ ^[1-9][0-9]{0,2}$ ]] || (( SEARCH_READY_ATTEMPTS > 300 )); then
    printf 'SEARCH_READY_ATTEMPTS must be an integer between 1 and 300.\n' >&2
    return 2
  fi
  if [[ ! "${SEARCH_READY_INTERVAL}" =~ ^(0|[1-9][0-9]?)$ ]] || (( SEARCH_READY_INTERVAL > 60 )); then
    printf 'SEARCH_READY_INTERVAL must be an integer between 0 and 60 seconds.\n' >&2
    return 2
  fi
}

search_ready() {
  docker exec "${SEARCH_CONTAINER}" curl -fsS --max-time 5 \
    'http://localhost:9200/_cluster/health?wait_for_status=yellow&timeout=1s' >/dev/null 2>&1
}

wait_ready() {
  local attempt
  for (( attempt=1; attempt<=SEARCH_READY_ATTEMPTS; attempt++ )); do
    if search_ready; then
      return 0
    fi
    if (( attempt < SEARCH_READY_ATTEMPTS )); then
      sleep "${SEARCH_READY_INTERVAL}"
    fi
  done
  printf 'Elasticsearch did not become ready after %s attempts.\n' "${SEARCH_READY_ATTEMPTS}" >&2
  docker logs --tail=80 "${SEARCH_CONTAINER}" >&2 || true
  return 1
}

case "${1:-start}" in
  start)
    validate_wait_settings
    docker network inspect "${SEARCH_NETWORK}" >/dev/null 2>&1 || docker network create "${SEARCH_NETWORK}" >/dev/null
    docker volume inspect "${SEARCH_VOLUME}" >/dev/null 2>&1 || docker volume create "${SEARCH_VOLUME}" >/dev/null
    if ! docker container inspect "${SEARCH_CONTAINER}" >/dev/null 2>&1; then
      docker run -d --name "${SEARCH_CONTAINER}" --restart unless-stopped \
        --network "${SEARCH_NETWORK}" --memory 2g \
        --ulimit nofile=65535:65535 \
        --health-cmd 'curl -fsS --max-time 5 "http://localhost:9200/_cluster/health?wait_for_status=yellow&timeout=1s" >/dev/null' \
        --health-interval 10s --health-timeout 6s --health-retries 12 --health-start-period 30s \
        -e discovery.type=single-node -e xpack.security.enabled=false \
        -e 'ES_JAVA_OPTS=-Xms512m -Xmx512m' \
        -v "${SEARCH_VOLUME}:/usr/share/elasticsearch/data" "${SEARCH_IMAGE}" >/dev/null
    else
      docker start "${SEARCH_CONTAINER}" >/dev/null
    fi
    attach_container "${SEARCH_CONTAINER}"
    wait_ready
    attach_container "${MANAGER_CONTAINER}"
    ;;
  attach-manager) attach_container "${MANAGER_CONTAINER}" ;;
  stop) docker stop "${SEARCH_CONTAINER}" ;;
  status) docker inspect "${SEARCH_CONTAINER}" --format '{{.State.Status}}' ;;
  health)
    if search_ready; then printf 'ready\n'; else printf 'not ready\n'; exit 1; fi
    ;;
  *) printf 'Usage: %s [start|attach-manager|stop|status|health]\n' "$0" >&2; exit 2 ;;
esac
