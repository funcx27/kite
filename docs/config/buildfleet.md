# Buildfleet Module (chart deployment)

Optional buildfleet module (pooled reproducible container builds), embedded in
the kite process. The `config.buildfleet` subtree is passed to the module's
config loader verbatim and unknown fields fail at startup; omit the section to
keep the module disabled.

- `store` should live under `/data` to reuse the existing persistent volume
  mount (same volume as the sqlite PVC). `/data` only exists when
  `db.sqlite.persistence` is enabled — with postgres/mysql, or persistence
  disabled, it is ephemeral container storage and blobs do not survive pod
  restarts.
- Multi-replica is not supported (in-memory runtime pool + local store):
  `replicaCount` must stay `1`.
- Providers are **not** configured here: in the chart deployment, managed
  providers live in the database and are registered and edited through the
  platform build settings UI; builds fail per request until one is added. A
  yaml `providers:` list only applies to standalone buildfleetd deployments
  (see the module's `buildfleet.example.yaml`).

```yaml
config:
  buildfleet:
    store: /data/buildfleet-store
```

See [chart-values.md](./chart-values.md) for the core chart values.
