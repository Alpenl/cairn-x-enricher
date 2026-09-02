# Container deployment notes

The image contains one static binary, the CA certificate bundle, and no shell. It runs as UID/GID `65532`, exposes port `8080`, and uses its own `healthcheck` command.

Runtime secrets must be injected as environment variables. Never provide `XAI_API_KEY` or `CAIRN_ENRICHER_TOKEN` as Docker build arguments.

```bash
export IMAGE_TAG=0.1.0
docker compose pull
docker compose up -d
docker compose ps
```

The compose profile binds health endpoints to loopback only, drops all Linux capabilities, enables `no-new-privileges`, and mounts the root filesystem read-only.
