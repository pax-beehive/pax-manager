#!/usr/bin/env bash
set -euo pipefail

# Private single-node search. No host port is published.
SEARCH_CONTAINER="${SEARCH_CONTAINER:-pax-search}"
SEARCH_NETWORK="${SEARCH_NETWORK:-pax-search-private}"
SEARCH_VOLUME="${SEARCH_VOLUME:-pax-search-data}"
SEARCH_IMAGE="${SEARCH_IMAGE:-docker.elastic.co/elasticsearch/elasticsearch:9.5.5}"
MANAGER_CONTAINER="${MANAGER_CONTAINER:-pax-manager}"

attach_manager() {
  if docker container inspect "${MANAGER_CONTAINER}" >/dev/null 2>&1; then
    if ! docker network inspect "${SEARCH_NETWORK}" --format '{{range .Containers}}{{println .Name}}{{end}}' | grep -Fxq "${MANAGER_CONTAINER}"; then
      docker network connect "${SEARCH_NETWORK}" "${MANAGER_CONTAINER}"
    fi
  fi
}

case "${1:-start}" in
  start)
    docker network inspect "${SEARCH_NETWORK}" >/dev/null 2>&1 || docker network create "${SEARCH_NETWORK}" >/dev/null
    docker volume inspect "${SEARCH_VOLUME}" >/dev/null 2>&1 || docker volume create "${SEARCH_VOLUME}" >/dev/null
    if ! docker container inspect "${SEARCH_CONTAINER}" >/dev/null 2>&1; then
      docker run -d --name "${SEARCH_CONTAINER}" --restart unless-stopped \
        --network "${SEARCH_NETWORK}" --memory 2g \
        -e discovery.type=single-node -e xpack.security.enabled=false \
        -e 'ES_JAVA_OPTS=-Xms512m -Xmx512m' \
        -v "${SEARCH_VOLUME}:/usr/share/elasticsearch/data" "${SEARCH_IMAGE}" >/dev/null
    else
      docker start "${SEARCH_CONTAINER}" >/dev/null
    fi
    attach_manager
    ;;
  attach-manager) attach_manager ;;
  stop) docker stop "${SEARCH_CONTAINER}" ;;
  status) docker inspect "${SEARCH_CONTAINER}" --format '{{.State.Status}}' ;;
  *) printf 'Usage: %s [start|attach-manager|stop|status]\n' "$0" >&2; exit 2 ;;
esac
