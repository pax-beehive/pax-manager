# Pax observability

Run this Compose project on `home_server`. Prometheus scrapes the Manager's
private HTTPS metrics listener and an optional Node Exporter on `home_dev`
every 30 seconds. Grafana queries Prometheus over the Compose network. Its local
port defaults to 3030. The Pax Manager dashboard shows API traffic and latency,
Manager CPU and memory, open WebSockets, and the host's CPU, memory, and
filesystem capacity for `/` and `/srv/bulk`.

## Deploy

Keep the scrape token, metrics CA certificate, and Grafana initial admin
password outside the checkout. Set these environment variables to absolute
paths on `home_server` before running Compose.

The stack uses the dedicated Docker subnet `10.202.91.0/28`. Set
`PAX_OBSERVABILITY_SUBNET` if that range overlaps a route on the monitoring host.

| Variable | Required file |
| --- | --- |
| `PAX_METRICS_TOKEN_FILE` | Raw token used by the Manager metrics listener |
| `PAX_METRICS_CA_FILE` | PEM certificate of the CA that issued the Manager metrics certificate |
| `GRAFANA_ADMIN_PASSWORD_FILE` | Strong initial password for the Grafana `admin` user |

Keep the files in a directory owned by your account with mode `0700`. Set file
mode `0444` so the non-root containers can read the individually mounted files.
The directory permissions restrict access to the originals on the host.

The default target in `targets.json` is `192.168.0.101:9109`. To use a different
target, set `PAX_METRICS_TARGET_FILE` to a file of the same format. Use an
address present in the metrics certificate's subject alternative names.

```sh
cd deploy/observability
docker compose config --quiet
docker compose pull
docker compose run --rm --no-deps --entrypoint promtool prometheus check config /etc/prometheus/prometheus.yml
docker compose up -d
```

After a scrape interval, check Manager connectivity from the Prometheus
container and verify that Grafana is ready. If host scraping is enabled, check
that the `pax-home-dev` target is up as well.

```sh
docker compose exec -T prometheus wget -qO- 'http://localhost:9090/api/v1/targets?state=active'
curl -fsS http://127.0.0.1:3030/api/health
```

Use an SSH tunnel from your workstation to open the dashboard.

```sh
ssh -NT -L 3030:127.0.0.1:3030 home_server
```

Open `http://127.0.0.1:3030/d/pax-manager` and log in as `admin`.
Set `GRAFANA_HOST_PORT` before running Compose if the server needs a different
port. Use the same host port in the health check and SSH forwarding destination.
Grafana reads its initial admin password when it creates the database. Rotate
the password in Grafana after initial setup.

## Storage and updates

Prometheus expires data after 14 days or when the 2 GiB retention budget
applies. Leave extra disk space for WAL, compaction, and images. Compose stores
Prometheus history and Grafana state in separate named volumes.

Run `docker compose up -d --force-recreate` after replacing a bound config or
secret file. To move the stack, copy the configuration and credentials and
back up the two named volumes. Stop the services before copying live volume
files.

## Enable metrics on a Manager

Publish container port 9109 on the private host interface. Mount the following
files read-only at their named paths in the container:

| Container path | Contents |
| --- | --- |
| `/run/pax-metrics/scrape-peer` | Scraper IP address, such as `192.168.0.100` |
| `/run/pax-metrics/scrape-token` | Raw bearer token shared with Prometheus |
| `/run/pax-metrics/server.pem` | Server certificate with the target IP as a SAN |
| `/run/pax-metrics/server.key` | Server certificate private key |

Provide the port and mounts through the regular deployment configuration.
In Manager's admin Feature Gates, set **Prometheus metrics** (`feature.metrics`)
to **GA** for the region. The gate defaults to Off and takes effect at runtime.
An off gate returns HTTP 404 and pauses HTTP instrumentation. Prometheus must
still pass the source IP and bearer token checks after the gate is enabled.
A Manager without provisioned files continues to run its business API.

## Enable home_dev host metrics

Install the [Node Exporter service](node-exporter/README.md) on `home_dev`.
The exporter uses Basic authentication with the user `prometheus` and the
password from `PAX_METRICS_TOKEN_FILE`. Its HTTPS certificate must cover the
target address and chain to `PAX_METRICS_CA_FILE`.

Set `PAX_HOME_DEV_TARGET_FILE` on `home_server` to a readable JSON file with the
Node Exporter address, then recreate Prometheus. The bundled target list is
empty until a host exporter is provisioned.

```json
[
  {"targets": ["192.168.0.101:9110"]}
]
```
