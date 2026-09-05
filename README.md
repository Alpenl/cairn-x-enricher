# Cairn X Enricher

一个独立于 Cairn Share App 的 Go 后台服务。它定时从 Cairn Share 的 Cloudflare Worker 领取尚未处理的 X 收藏，用 Grok Responses API 和服务端 `x_search` 读取原帖及评论，再把 AI 中文标题、原始语言全文、完整简体中文译文、摘要和内容相关链接写回 D1；相关图片复制到 Cloudflare R2。

现有 App 不需要更新：原有 `/api/links` 请求、响应和鉴权均保持不变。新增队列字段和 `/api/enrichment/*` 内部接口只服务于本项目。

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

## 本地运行

```bash
go test ./...
go run ./cmd/cairn-x-enricher once --max-jobs 10
go run ./cmd/cairn-x-enricher serve
```

`serve` 启动后立即执行一批任务，之后按 `POLL_INTERVAL` 运行，并在 `127.0.0.1:8080` 暴露：

- `/`：内容优先的收藏首页。最近四条配图展示，其余按时间分组成三列速览，向下滚动继续加载；顶部搜索框直接搜标题、备注、摘要和译文，命中处高亮。
- `/bookmarks/{id}`：阅读页，展示 AI 标题、手动备注、图片、摘要和中文全文，原文默认收起。
- `/backstage`：后台页，只展示服务状态和需要人工处理的失败收藏，平时不需要打开。
- `/api/backstage`：后台页使用的聚合状态，统一返回最近处理记录、失败计数和可手动重试条目。
- `/api/bookmarks`：处理台的同源收藏列表代理。
- `/api/bookmarks/{id}`：包含完整原文的单条详情。
- `/api/images/{key...}`：受控的 R2 图片同源代理。
- `/api/bookmarks/process`：提交最多 10 个收藏 ID 立即处理。
- `/api/bookmarks/{id}/source`：提交人工补充的原帖正文，绕过 X Search 直接生成标题、语言、译文和摘要。
- `/healthz`：进程存活。
- `/readyz`：服务就绪。
- `/status`：最近一批的匿名统计和错误状态。

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
