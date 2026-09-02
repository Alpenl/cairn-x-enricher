# Cloudflare Backend Contract

配套改造位于 Cairn Share 仓库：

- `worker/migrations/0005_add_x_enrichment.sql`
- `worker/src/index.ts`
- `worker/test/index.test.ts`

## 新增字段

| 字段组 | 字段 |
| --- | --- |
| 队列 | `enrichment_status`, `enrichment_attempts`, `enrichment_next_retry_at` |
| Lease | `enrichment_lease_token`, `enrichment_lease_until` |
| 结果 | `original_text`, `summary`, `related_links`, `enrichment_model` |
| 诊断 | `enrichment_error`, `enrichment_updated_at`, `enriched_at` |

迁移只有 `ADD COLUMN` 和新增索引，不删除或改名现有字段。原 App API 仍显式只选择并返回 `id, url, note, created_at, learned, learned_at`。

## 内部 API

所有接口都要求独立的 `CAIRN_ENRICHER_TOKEN`：

| 请求 | 成功响应 | 用途 |
| --- | --- | --- |
| `POST /api/enrichment/jobs/claim` | `200` job 或 `204` | 原子领取最早的 X 链接 |
| `POST /api/enrichment/jobs/{id}/complete` | `200` | 以匹配 lease 写入结果 |
| `POST /api/enrichment/jobs/{id}/fail` | `200` | 记录失败并计算退避时间 |

不要把内部 token 配置成 `CAIRN_API_TOKEN`，也不要把这些内部响应暴露给 App。

## 部署命令

在 Cairn Share 的 `worker/` 目录执行：

```bash
npx wrangler secret put CAIRN_ENRICHER_TOKEN
npx wrangler d1 migrations apply cairn-share-db --remote
npm test
npm run typecheck
npx wrangler deploy
```

迁移和 Worker 部署完成后再启动 Enricher，否则 claim 会返回 `404` 或 `auth_not_configured`。
