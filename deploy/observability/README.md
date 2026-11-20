# Pax observability

Run this Compose project on `home_server`. Prometheus scrapes the Manager's
private HTTPS metrics listener on `home_dev` every 30 seconds. Grafana queries
Prometheus over the Compose network. Its local port defaults to 3030.

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
container and verify that Grafana is ready.

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
