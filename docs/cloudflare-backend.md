# Cloudflare Backend Contract

配套改造位于 Cairn Share 仓库：

- `worker/migrations/0005_add_x_enrichment.sql`
- `worker/migrations/0006_add_rich_x_enrichment.sql`
- `worker/src/index.ts`
- `worker/test/index.test.ts`
- `worker/wrangler.jsonc`

## 新增字段

| 字段组 | 字段 |
| --- | --- |
| 队列 | `enrichment_status`, `enrichment_attempts`, `enrichment_next_retry_at` |
| Lease | `enrichment_lease_token`, `enrichment_lease_until` |
| 结果 | `ai_title`, `original_language`, `original_text`, `translated_text`, `summary`, `related_links`, `images`, `enrichment_model` |
| 诊断 | `enrichment_error`, `enrichment_updated_at`, `enriched_at` |

迁移只有 `ADD COLUMN` 和新增索引，不删除或改名现有字段。原 App API 仍显式只选择并返回 `id, url, note, created_at, learned, learned_at`。

Worker 绑定名为 `ENRICHMENT_IMAGES` 的 R2 bucket，生产 bucket 名为 `cairn-x-enrichment-images`。D1 的 `images` 字段只保存经过校验的对象 key 和 MIME，不保存外部图片 URL。

## 内部 API

所有接口都要求独立的 `CAIRN_ENRICHER_TOKEN`：

| 请求 | 成功响应 | 用途 |
| --- | --- | --- |
| `GET /api/enrichment/jobs` | `200` page | 分页列出全部收藏、原文、处理状态和分类总数 |
| `POST /api/enrichment/jobs/claim` | `200` job 或 `204` | 原子领取最早的 X 链接 |
| `GET /api/enrichment/jobs/{id}` | `200` detail | 读取单条收藏及完整原文 |
| `POST /api/enrichment/jobs/{id}/claim` | `200` job | 原子领取指定收藏用于人工处理或重新处理 |
| `POST /api/enrichment/jobs/{id}/images` | `200` refs | 在匹配 lease 下抓取允许的 X 图片并写入 R2 |
| `POST /api/enrichment/jobs/{id}/complete` | `200` | 以匹配 lease 写入结果 |
| `POST /api/enrichment/jobs/{id}/fail` | `200` | 记录失败并计算退避时间 |
| `GET /api/enrichment/images/{key...}` | `200` image | 读取一个受控 R2 对象供 NAS 代理 |

指定领取会为新的人工处理周期重置尝试次数，但保留旧结果直到新结果成功写入；有效的 `processing` lease 返回 `409 job_busy`，避免重复模型调用。列表不返回 lease token，但会返回阅读所需的已保存内容、`processable` 标志和 `unsupported` 计数。非 X 收藏可见但不能领取处理。

图片抓取只接受 HTTPS `pbs.twimg.com/media`，不跟随重定向，最多 8 张、单张最多 15 MiB，并限制为 JPEG、PNG、WebP、GIF 或 AVIF。R2 读取接口仍要求内部 token；浏览器只访问 NAS 的同源代理。

不要把内部 token 配置成 `CAIRN_API_TOKEN`，也不要把这些内部响应暴露给 App。NAS 页面经 Go 同源 API 使用这些接口，浏览器不持有内部 token。

## 部署命令

在 Cairn Share 的 `worker/` 目录执行：

```bash
npx wrangler r2 bucket create cairn-x-enrichment-images
npx wrangler secret put CAIRN_ENRICHER_TOKEN
npx wrangler d1 migrations apply cairn-share --remote
npm test
npm run typecheck
npx wrangler deploy
```

R2 bucket 只需创建一次。迁移和 Worker 部署完成后再启动 `v0.3.0` Enricher，否则富内容写入或图片接口会失败。新增完成字段为可选，迁移与部署之间的短暂窗口仍兼容旧服务。
