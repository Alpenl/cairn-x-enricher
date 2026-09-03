# Cairn X Enricher

一个独立于 Cairn Share App 的 Go 后台服务。它定时从 Cairn Share 的 Cloudflare Worker 领取尚未处理的 X 收藏，用 Grok 的 Responses API 和服务端 `x_search` 读取原帖及评论，再把原文、简短总结和内容相关链接写回 D1。

现有 App 不需要更新：原有 `/api/links` 请求、响应和鉴权均保持不变。新增队列字段和 `/api/enrichment/*` 内部接口只服务于本项目。

## 数据流

```text
Cairn Share App -> 原有 Worker API -> D1 links
                                      |
                                      v
定时器 -> 内部 claim API -> Eino 工作流 -> Grok /responses + x_search
  ^                                              |
  +--------- complete/fail API <- 校验后的 JSON -+
```

核心保证：

- Worker 用原子更新和 15 分钟 lease 分发任务，支持多实例并发而不重复领取。
- 成功结果只有在响应包含已完成的 X Search 证据且通过严格 JSON/URL 校验后才写入。
- 失败由 Worker 按 `1m / 5m / 30m / 2h` 退避，最多尝试 5 次。
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

- `/`：只读运行状态页面，每 10 秒刷新一次匿名统计。
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
Momax NAS 使用 [deploy/nas/compose.yaml](deploy/nas/compose.yaml)，局域网状态页映射到 `8088`；该清单只拉取 GitHub Actions 发布的镜像，不在 NAS 本地构建。

## 发布

所有构建都在 GitHub Actions 完成：

- 每次 push/PR：模块校验、静态检查、race test、二进制构建、双架构 Docker 构建。
- 推送 `vX.Y.Z` tag：GoReleaser 创建 GitHub Release，上传六个平台压缩包和 checksum，同时发布带 provenance/SBOM 的 GHCR 多架构镜像。
- Release 正文来自 `CHANGELOG.md` 对应版本，Action 和工具版本均固定。

## 安全

不要提交 `.env`。任何曾出现在聊天、终端历史或日志中的 API key 都应立即轮换，再更新运行环境。漏洞报告流程见 [SECURITY.md](SECURITY.md)。

## License

[MIT](LICENSE)
