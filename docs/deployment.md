# Deployment

## 1. 部署 Cloudflare 前置改造

先部署 Cairn Share Worker 的 `0005_add_x_enrichment.sql`、`0006_add_rich_x_enrichment.sql` 和内部 API，创建并绑定 `cairn-x-enrichment-images` R2 bucket。把一个新生成的随机值同时配置为 Worker secret `CAIRN_ENRICHER_TOKEN` 与服务端环境变量，不要复用 App 的 `CAIRN_API_TOKEN`。

## 2. 准备运行配置

```bash
cp .env.example .env
chmod 600 .env
```

至少填入 `CAIRN_ENRICHER_TOKEN` 和 `XAI_API_KEY`。生产平台应使用 secret manager 注入，而不是上传 `.env`。

## 3. 启动固定版本镜像

```bash
export IMAGE_TAG=0.3.0
docker compose pull
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:8080/status
```

服务启动即执行第一批，默认每 5 分钟再运行。部署多个副本是安全的：D1 claim 是原子的，每项任务还有独立 lease。

## 4. 回滚

把 `IMAGE_TAG` 改回上一个 GitHub Release 的版本并重新 `docker compose up -d`。新增 D1 列保持向后兼容，无需在应用回滚时删除。停止所有实例不会丢任务；未完成 lease 到期后会重新进入可领取状态。

## 5. 观测

- 容器健康：`GET /healthz`。
- 最近批次：`GET /status`。
- 中文收藏列表：`GET /`；独立阅读页：`GET /bookmarks/{id}`；只允许通过可信局域网访问。
- 收藏列表：`GET /api/bookmarks`；人工处理：`POST /api/bookmarks/process`。
- 图片代理：`GET /api/images/{key...}`；对象本体位于 Cloudflare R2。
- 日志：结构化 JSON，按 `link_id` 和 `attempt` 关联，不记录凭据或模型原文。
- D1：检查 `enrichment_status`、`enrichment_error` 和 `enrichment_updated_at`。
