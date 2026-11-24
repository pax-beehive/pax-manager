# Host metrics on home_dev

Install the verified Linux Node Exporter binary at
`~/.local/bin/pax-node-exporter` and the included unit under
`~/.config/systemd/user/` on home_dev. The user manager must be running after
logout, as it already is for the release agent. This service uses an unprivileged
account and listens only on the configured LAN interface.

Create `~/.config/pax-node-exporter/web.yml` with mode 0600. Use absolute paths
to the Manager metrics serving certificate and private key. Configure the
`prometheus` user with a bcrypt hash of the monitoring stack's scrape token.

```yaml
tls_server_config:
  cert_file: /absolute/path/to/server.pem
  key_file: /absolute/path/to/server.key
basic_auth_users:
  prometheus: '<bcrypt hash>'
```

Start the service on home_dev with:

```sh
systemctl --user daemon-reload
systemctl --user enable --now pax-node-exporter.service
```

Configure the optional home_dev scrape target on the monitoring host. Keep
the exporter off the public tunnel. The unit collects CPU and I/O wait,
available memory, disk I/O, and utilization of the root and bulk filesystems.
