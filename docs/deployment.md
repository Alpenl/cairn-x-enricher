# Deployment

## 1. 部署 Cloudflare 前置改造

首次部署时创建并绑定 `cairn-x-enrichment-images` R2 bucket，把一个新生成的随机值同时配置为 Worker secret `CAIRN_ENRICHER_TOKEN` 与服务端环境变量，不要复用 App 的 `CAIRN_API_TOKEN`。升级时沿用现有 bucket 和 secret。

`v0.5.0` 需要 Cairn Share Worker 的全部迁移（截至 `0007_add_bookmark_curation.sql`）和配套内部 API。旧版 Go 客户端不能解析新版 Worker 的列表字段，新版 Go 启动时需要读取 Worker 词表，因此升级两端需要协调：

1. 确认 GitHub Actions 已发布 `0.5.0` 镜像，提前在 NAS 拉取；记录当前 Worker version ID、NAS 镜像 digest 和 Compose 配置。
2. 应用远程 D1 迁移。迁移只增加列和索引，原有数据与 App 接口继续可用。
3. 等当前处理批次结束后停止旧 Enricher，部署新版 Worker，立即启动 `0.5.0` Enricher。
4. 检查词表、收藏列表、阅读页、筛选和服务健康，再确认后台队列正常。

迁移和 Worker 发布命令见 [Cloudflare 后端说明](cloudflare-backend.md)。服务切换期间阅读库短暂不可用，App 收藏入口仍由 Worker 提供。不要启动额外的历史全库处理任务；本版本不自动回填旧收藏。

## 2. 准备运行配置

```bash
cp .env.example .env
chmod 600 .env
```

至少填入 `CAIRN_ENRICHER_TOKEN` 和 `XAI_API_KEY`。生产平台应使用 secret manager 注入，而不是上传 `.env`。

## 3. 启动固定版本镜像

```bash
export IMAGE_TAG=0.5.0
docker compose pull
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:8080/status
```

服务启动即执行第一批，默认每 5 分钟再运行。部署多个副本是安全的：D1 claim 是原子的，每项任务还有独立 lease。

Momax NAS 使用 `deploy/nas/compose.yaml` 中的固定镜像，访问端口为 `8088`。保留部署目录原有的 `.env`，更新 Compose 后在该目录执行 `docker compose pull` 和 `docker compose up -d`。

## 4. 回滚

从 `v0.5.0` 回滚到 `v0.4.1` 时需要同时回滚 Worker 与 Enricher：先停止新版 Enricher，用 `npx wrangler rollback <previous-worker-version-id>` 恢复部署前记录的 Worker 版本，再恢复原有 Compose 和镜像并启动。只回滚 NAS 镜像会导致旧版 Go 无法解析新版 Worker 响应。

新增 D1 列保留，不执行删除列或数据恢复；旧 Worker 使用显式字段查询，能与新增列共存。未完成的 lease 到期后会重新进入可领取状态。回滚前后均检查 `/healthz`、收藏列表和后台处理状态。

## 5. 观测

- 容器健康：`GET /healthz`（进程存活，不依赖上游）。
- 服务就绪：`GET /readyz`；启动自检或模型契约检查失败时为 `503`，`ready_reason` 说明原因。
- 最近批次：`GET /status`。
- 中文收藏列表：`GET /`；独立阅读页：`GET /bookmarks/{id}`；只允许通过可信局域网访问。
- 收藏列表：`GET /api/bookmarks`；人工处理：`POST /api/bookmarks/process`。
- 固定词表：`GET /api/taxonomy`；应返回 `2026-09-08.1` 版本。
- 图片代理：`GET /api/images/{key...}`；对象本体位于 Cloudflare R2。
- 日志：结构化 JSON，按 `link_id` 和 `attempt` 关联，不记录凭据或模型原文。
- D1：检查 `enrichment_status`、`enrichment_error` 和 `enrichment_updated_at`。
