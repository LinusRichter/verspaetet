# verspaetet monitoring (Grafana + VictoriaMetrics)

Ops-Monitoring-Stack für verspaetet: ein Ampel-Dashboard mit Verläufen für
Fetching, Persistenz, Queue, Exports, Host- und Container-Metriken.

Der Stack wird über `include:` aus der Root-`docker-compose.yml` in dasselbe
Compose-Projekt gezogen — ein Netzwerk, ein Deploy, keine geteilten Ports
(außer Grafana :8082).

## Services

| Service | Port | Zweck |
|---|---|---|
| `grafana` | **:8082** (published) | Ops-Dashboard, provisioniert aus `grafana/` |
| `victoriametrics` | 127.0.0.1:8428 | Zeitreihen-Storage + Scrape (UI: `/vmui`) |
| `node_exporter` | intern | Host CPU/RAM/Disk/Netz + textfile collector |
| `cadvisor` | intern | CPU/RAM pro Container |

## Starten

```bash
docker compose up -d grafana victoriametrics node_exporter cadvisor
```

Credentials: `GRAFANA_ADMIN_USER` / `GRAFANA_ADMIN_PASSWORD` in `.env`
(Defaults: admin/admin — im Betrieb setzen!). Nach dem ersten Start:
`docker compose restart grafana` falls das Passwort nachgezogen wurde.

## Dashboard

`grafana/dashboards/verspaetet-ops.json` wird beim Start provisioniert
(dashboards as code — Änderungen: Datei editieren, Grafana lädt alle 30 s neu).
Datasource `VictoriaMetrics` ist ebenfalls provisioniert.

## App-Metriken

Die Go-Binaries (worker, scheduler) exponieren `/metrics` auf :9091
(`METRICS_ADDR` env), die api auf :8080. Alle Metriken sind im Package
`metrics/` definiert; Namensschema `verspaetet_*`. Scrape-Config: `scrape.yml`.

## Backup-Heartbeat

`node_exporter` liest `textfile/*.prom`. Ein Backup-Job schreibt am Ende
(das Muster zeigt `backup-example.sh`):

```
verspaetet_backup_last_success_timestamp 1700000000
```

Ohne Datei/Update färbt sich das Panel „Backup age" nach 2 Tagen gelb,
nach 7 Tagen rot. Gleiches Muster für beliebige Cron-Jobs.

## Hinweise

- node_exporter/cadvisor mounten Host-Pfade — auf Linux-Hosts (Deployment)
  voll funktionsfähig; unter Docker Desktop eingeschränkt (rootfs des VM-Kernels).
- Retention: 90 Tage (`-retentionPeriod=90d`), Scrape-Intervall 30 s.
- Keine Secrets in diesem Ordner — alles kommt aus `.env`.