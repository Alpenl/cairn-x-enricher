# Cairn X Enricher

一个独立于 Cairn Share App 的 Go 后台服务。它定时从 Cairn Share 的 Cloudflare Worker 领取尚未处理的 X 收藏，用 Grok Responses API 和服务端 `x_search` 读取原帖及评论，再把 AI 中文标题、原始语言全文、完整简体中文译文、摘要和内容相关链接写回 D1；相关图片复制到 Cloudflare R2。

现有 App 不需要更新：原有 `/api/links` 请求、响应和鉴权均保持不变。新增队列字段和 `/api/enrichment/*` 内部接口只服务于本项目。

收藏管理支持固定词表分类、人工收藏原因、收件箱/精选/笔记/搁置状态、组合筛选及 Markdown 导出。实现依据的 [Grok 完整讨论与原始手册](docs/bookmark-management.md#讨论归档) 已保存到仓库，具体行为、词表维护和配套升级见 [收藏管理说明](docs/bookmark-management.md)。

## 数据流

```text
Cairn Share App -> 原有 Worker API -> D1 links
                                      |
                                      v
定时器 -> 内部 claim API -> Eino 工作流 -> Grok /responses + x_search
  ^                                              |
  +--- complete/fail API <- 校验后的 JSON + R2 图片归档

浏览器 -> NAS 收藏首页 -> 阅读页/搜索/后台重试 -> 同一处理流程
```

核心保证：

- Worker 用原子更新和 15 分钟 lease 分发任务，支持多实例并发而不重复领取。
- 成功结果只有在响应包含已完成的 X Search 证据且通过严格 JSON/URL 校验后才写入。
- 图片只接受 `https://pbs.twimg.com/media/...`，由 Worker 校验响应类型与大小后写入 R2，浏览器不接触 Cloudflare token。
- 失败由 Worker 按 `1m / 5m / 30m / 2h` 退避，最多尝试 5 次。
- 模型端点的临时 `408/429/5xx` 会在单次队列 attempt 内短重试；如果完整线程读取慢失败，会降级为只读原帖的结构化请求，避免上游抖动直接耗尽业务重试次数。
- 失败或耗尽记录如果已经保留 `original_text`，会直接用现有原文补齐标题、语言、译文和摘要；后台也支持粘贴原文后生成。
- URL 或备注被 App 修改时，已有增强结果自动失效并重新入队。
- 日志不会输出 API key、完整提示词或模型响应。
- 标签只从 Worker 提供的版本化词表中选择，未知标签被丢弃并标记待确认；人工整理结果不会被重新处理覆盖。

## 为什么使用 Eino

项目使用 [CloudWeGo Eino](https://github.com/cloudwego/eino) 的类型化工作流组织“模型调用 -> 结果校验”。xAI 的 `x_search` 是 Responses API 的服务端工具，现成 OpenAI Go 适配器尚不能完整解析 `x_search_call`，因此 wire protocol 由一个窄适配层负责；调度、lease、重试和数据库事务仍是普通 Go 代码。详细选型证据见 [docs/research/go-agent-frameworks.md](docs/research/go-agent-frameworks.md)。

## 配置

```bash
cp .env.example .env
chmod 600 .env
```

必须配置：

| 变量 | 含义 |
| --- | --- |
| `CAIRN_ENRICHER_TOKEN` | Worker 内部接口专用 Bearer token，不能复用 App token |
| `GROK_MODELS_BASE_URL` | Responses-compatible API 根地址，包含 `/v1` |
| `XAI_API_KEY` | 模型端点密钥 |

其余变量及默认值均列在 `.env.example`。进程启动时会验证必填值、URL、数值范围和 duration 格式。

启动时还会做两项前置检查，任何一项失败都会让进程以非零码退出并在日志中给出原因：

1. 从 Worker 读取并校验版本化词表（需要配套的 curation 迁移和内部接口）。
2. 用一次极短的 `source` 请求做模型契约自检（canary），确认目标端点仍然遵守 strict JSON Schema。端点、模型名或密钥配错时，服务会立即失败，而不是静默耗尽每一条收藏的重试次数。

HTTP 服务在这两项检查通过后才开始监听，因此配置错误表现为“容器启动即退出 + 日志中的明确原因”，而不是一个长期返回 `503` 的半死进程。相反，运行期发生故障时 HTTP 服务仍会保持监听：`/healthz` 返回 `200`，`/readyz` 返回 `503` 并带上原因。

## 本地运行

```bash
go test ./...
make test-frontend   # 零依赖的前端检查，只需 Node
make verify          # vet + golangci-lint + 上述两项 + 构建
go run ./cmd/cairn-x-enricher once --max-jobs 10
go run ./cmd/cairn-x-enricher serve
```

`serve` 启动后立即执行一批任务，之后按 `POLL_INTERVAL` 运行，并在 `127.0.0.1:8080` 暴露：

- `/`：收藏首页，支持主题、形态、用途、来源、整理状态和时间筛选；搜索覆盖原文、译文、摘要、实体、备注及收藏原因，命中处高亮；当前加载结果可导出 Markdown。
- `/bookmarks/{id}`：阅读页，展示标题、图片、摘要和全文，原文默认收起；可确认分类、填写收藏原因、修改整理状态并导出单条收藏。
- `/backstage`：后台页，只展示服务状态和需要人工处理的失败收藏，平时不需要打开。
- `/api/backstage`：后台页使用的聚合状态，统一返回最近处理记录、失败计数和可手动重试条目。
- `/api/bookmarks`：处理台的同源收藏列表代理。
- `/api/bookmarks/{id}`：包含完整原文的单条详情。
- `/api/taxonomy`：当前标签词表的同源接口。
- `/api/bookmarks/{id}/curation`：通过 `PATCH` 保存人工整理，不调用模型。
- `/api/images/{key...}`：受控的 R2 图片同源代理。
- `/api/bookmarks/process`：提交最多 10 个收藏 ID 立即处理。
- `/api/bookmarks/{id}/source`：提交人工补充的原帖正文，绕过 X Search 直接生成标题、语言、译文和摘要。
- `/healthz`：进程存活。只要进程还在就不会失败，因此在 Worker 或模型不可达时也不会被重启策略反复杀死。
- `/readyz`：服务就绪。运行期被标记为降级时返回 `503`，响应体带上 `ready_reason` 与 `unhealthy_since`。
- `/status`：最近一批的匿名统计、错误状态、就绪原因和构建信息。

`once` 是适合 cron 和诊断的有界批处理命令，输出稳定 JSON；根命令不会隐式调用付费 API 或修改数据库。

## Docker

```bash
docker compose up -d --build
docker compose ps
curl -fsS http://127.0.0.1:8080/status
```

镜像是无 shell、非 root、只读文件系统的静态二进制，支持 `linux/amd64` 和 `linux/arm64`。正式版本发布到：

```text
ghcr.io/alpenl/cairn-x-enricher:<version>
```

完整部署顺序和 Cloudflare 前置改造见 [docs/deployment.md](docs/deployment.md) 与 [docs/cloudflare-backend.md](docs/cloudflare-backend.md)。
分类功能需要配套 Worker 的 `0007_add_bookmark_curation.sql` 及新接口。请配套升级两端；Enricher 启动时会读取并验证词表，旧 Worker 会导致启动失败。不会自动回填历史收藏。
Momax NAS 使用 [deploy/nas/compose.yaml](deploy/nas/compose.yaml)，局域网阅读库映射到 `8088`；页面展示 Cloudflare 中全部收藏，只有 X 链接可以触发模型处理。旧版已完成记录会继续显示原内容，只有手动重新处理后才会生成新版标题、译文和图片。该清单只拉取 GitHub Actions 发布的镜像，不在 NAS 本地构建。

## 发布

所有构建都在 GitHub Actions 完成：

- 每次 push/PR：模块校验、静态检查、race test、二进制构建、双架构 Docker 构建。
- 推送 `vX.Y.Z` tag：GoReleaser 创建 GitHub Release，上传六个平台压缩包和 checksum，同时发布带 provenance/SBOM 的 GHCR 多架构镜像。
- Release 正文来自 `CHANGELOG.md` 对应版本，Action 和工具版本均固定。

## 安全

不要提交 `.env`。处理台不向浏览器发送 Worker token 或模型密钥，但会展示收藏内容并允许触发付费模型请求，因此端口 `8088` 只应开放在可信局域网，不应配置公网端口转发。任何曾出现在聊天、终端历史或日志中的 API key 都应立即轮换，再更新运行环境。漏洞报告流程见 [SECURITY.md](SECURITY.md)。

## License

[MIT](LICENSE)
