# Cloudflare Backend Contract

当前部署以 [stack manifest](../deploy/stack-contract.json) 和 [部署合同](deployment.md) 为准，候选版本需要全部迁移至 `0050_classification_audit_archive.sql`。以下早期迁移列表用于说明字段来源，不是完整升级清单。

现代客户端显式协商能力，旧客户端保留原响应形状：

| 能力 | 请求头 / 接口 | 合同 |
| --- | --- | --- |
| 有效标签 | `X-Cairn-Tag-System: 1` | 列表和标签同一读取边界；持久事实驱动索引投影，精确 SQL 聚合计数 |
| 内容特征 | `X-Cairn-Content-Functions: 1` | `functions_mode=any/all`，旧默认 any |
| 搜索摘要 | `X-Cairn-Search-Summary: 1` | 非空 q 返回最多 240 Unicode 字符的 `search_excerpt`，不带全文；响应确认能力 |
| 最早未读队列 | `X-Cairn-Queue: 1` | 服务端稳定分页、全库计数，不从最新 5000 条推算 |
| 运行历史 | `X-Cairn-Run-History: 1`；`/api/v2/links/{id}/runs?view=summary` | 每页最多 50 条，倒序游标；`/runs/{run_id}` 按需读取完整冷/热记录 |
| 标签质量 | `GET /api/v2/tags/quality` | 明确来源的操作统计、同次纠正对及检索覆盖；不含正文，不自动修改策略 |
| 受控重放 | `POST /api/v2/links/{id}/policy-replays` | 完整 policy hash、个人版本/目标 generation CAS；保留内容/spec/模型校验，零模型调用 |

`Server-Timing` 暴露 Worker 总耗时与 D1 耗时，NAS 聚合为 `nas/upstream/worker/d1`。大 payload 冷归档保留轻量审计身份与可恢复引用。

配套改造位于 Cairn Share 仓库：

- `worker/migrations/0005_add_x_enrichment.sql`
- `worker/migrations/0006_add_rich_x_enrichment.sql`
- `worker/migrations/0007_add_bookmark_curation.sql`
- `worker/migrations/0008_invalidate_enriched_link_cache.sql`
- `worker/migrations/0009_independent_classification.sql`
- `worker/src/classification.ts`
- `worker/src/curation.ts`、`worker/src/taxonomy.json`
- `worker/src/index.ts`
- `worker/test/index.test.ts`
- `worker/test/app-enrichment.test.ts`
- `worker/wrangler.jsonc`

当前开发版还向 App 提供 `include=enrichment` 列表/详情、`/api/taxonomy`、
`/api/images/:key` 和 `PATCH /api/links/:id/curation`，使用独立 App Token。
内部列表支持 `view=summary`；详情保持完整。迁移 0008 让富化/整理更新事务同时递增
公共缓存版本，避免新版 App 读取过期内容。默认公共六字段响应继续兼容。

## 新增字段

| 字段组 | 字段 |
| --- | --- |
| 队列 | `enrichment_status`, `enrichment_attempts`, `enrichment_next_retry_at` |
| Lease | `enrichment_lease_token`, `enrichment_lease_until` |
| 结果 | `ai_title`, `original_language`, `original_text`, `translated_text`, `summary`, `related_links`, `images`, `enrichment_model` |
| 诊断 | `enrichment_error`, `enrichment_updated_at`, `enriched_at` |
| 收藏整理 | `classification`, `curation`, `why`, `curation_status` |

早期迁移增加阅读字段，后续还包括队列、事实、触发器、索引投影及归档元数据，升级必须演练。原 App 默认 API 仍显式只返回 `id, url, note, created_at, learned, learned_at`。

Worker 绑定名为 `ENRICHMENT_IMAGES` 的 R2 bucket，生产 bucket 名为 `cairn-x-enrichment-images`。D1 的 `images` 字段只保存经过校验的对象 key 和 MIME，不保存外部图片 URL。

## 内部 API

所有接口都要求独立的 `CAIRN_ENRICHER_TOKEN`：

| 请求 | 成功响应 | 用途 |
| --- | --- | --- |
| `GET /api/enrichment/jobs` | `200` page | 分页列出全部收藏、原文、处理状态和分类总数 |
| `GET /api/enrichment/taxonomy` | `200` catalog | 统一的版本化主题、形态、用途和别名词表 |
| `PATCH /api/enrichment/jobs/{id}/curation` | `200` detail | 保存人工原因、整理状态和分类，或恢复自动分类 |
| `POST /api/enrichment/jobs/claim` | `200` job 或 `204` | 原子领取最早的 X 链接 |
| `GET /api/enrichment/jobs/{id}` | `200` detail | 读取单条收藏及完整原文 |
| `GET /api/enrichment/jobs/{id}/source` | `200` snapshot 或 `204` | 获取仍匹配当前原文的来源快照 |
| `POST /api/enrichment/jobs/{id}/source` | `200` | 有效获取 lease 下存档原文并将分类入队 |
| `POST /api/enrichment/classifications/claim` | `200` job 或 `204` | 独立领取分类任务，提交词表/策略/请求模型版本 |
| `POST /api/enrichment/classifications/{id}/complete` | `200` | 按有效 lease、revision 和版本提交分类与审计信息 |
| `POST /api/enrichment/classifications/{id}/fail` | `200` | 只对分类任务退避，不改变正文状态 |
| `POST /api/enrichment/classifications/{id}/retry` | `200` | 对已有原文显式入队；活跃 lease 返回 `409` |
| `GET /api/enrichment/classifications/{id}` | `200` state | 返回独立状态、错误和已保存概率，不返回 lease token |
| `POST /api/enrichment/jobs/{id}/claim` | `200` job | 原子领取指定收藏用于人工处理或重新处理 |
| `POST /api/enrichment/jobs/{id}/images` | `200` refs | 在匹配 lease 下抓取允许的 X 图片并写入 R2 |
| `POST /api/enrichment/jobs/{id}/complete` | `200` | 以匹配 lease 写入结果 |
| `POST /api/enrichment/jobs/{id}/fail` | `200` | 记录失败并计算退避时间 |
| `GET /api/enrichment/images/{key...}` | `200` image | 读取一个受控 R2 对象供 NAS 代理 |

指定领取会为新的人工处理周期重置尝试次数，但保留旧结果直到新结果成功写入；有效的 `processing` lease 返回 `409 job_busy`，避免重复模型调用。列表不返回 lease token，但会返回阅读所需的已保存内容、`processable` 标志和 `unsupported` 计数。非 X 收藏可见但不能领取处理。

新版生产处理在阅读增强前写入原文快照。显式替换原文会清除不再对应的阅读增强；同一原文的旧图片引用保留。
迁移 0009 的 `enrichment_sources` 与 `classification_jobs` 不改变公开 App 响应字段。
现有 `complete` 接口仍兼容旧生成器，但新阅读增强请求不携带 `classification`。
所有新增接口仍要求内部 token。新版本必须先升级 Worker；详见 [Jev 分类说明](jev-classification.md)。

图片抓取只接受 HTTPS `pbs.twimg.com/media`，不跟随重定向，最多 8 张、单张最多 15 MiB，并限制为 JPEG、PNG、WebP、GIF 或 AVIF。R2 读取接口仍要求内部 token；浏览器只访问 NAS 的同源代理。

不要把内部 token 配置成 `CAIRN_API_TOKEN`，也不要把这些内部响应暴露给 App。NAS 页面经 Go 同源 API 使用这些接口，浏览器不持有内部 token。

收藏分类与整理的完整字段语义、过滤参数和词表维护见 [收藏管理说明](bookmark-management.md)。升级使用固定 stack manifest 并应用全部待执行迁移；新增字段通过能力协商保护旧客户端。

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

R2 bucket 和 secret 只在首次部署时创建，已有部署无需重复创建或轮换。上述原子命令不能代替发布门禁；当前固定版本、恢复记录、迁移演练、暂停消费者和只读验收顺序见 [部署说明](deployment.md)。
