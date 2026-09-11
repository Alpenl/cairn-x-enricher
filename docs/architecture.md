# Architecture

## 边界

本服务是独立部署单元，不链接、不打包也不修改 Android App。它通过 Cairn Share Worker 的内部 HTTP 操作读取收藏、领取任务、完成和上报失败。Worker 是 D1 前面的唯一数据访问层，避免把 Cloudflare 控制面 D1 REST API 当作高频应用数据 API。

## 一次任务的状态变化

```text
pending
  -> processing (attempt + 1, lease token, lease deadline)
  -> completed
  -> failed -> pending eligibility after next_retry_at
  -> exhausted after attempt 5
```

过期的 `processing` lease 可被重新领取。完成和失败写入均要求当前 lease token 匹配，因此旧实例不能覆盖新实例的结果。

## 关停与 panic 隔离

`SHUTDOWN_TIMEOUT` 是**整个**优雅关停的预算，不是每个阶段的预算：HTTP 停止接受连接与等待在途任务共用同一份 deadline，并且单次定时批处理最多只用其中一半，另一半留给 `runServe` 等这一批结束。两个 compose 文件把 `stop_grace_period` 设为 30s，必须大于 `SHUTDOWN_TIMEOUT`，否则 Docker 会在排空完成前 SIGKILL。

排空是 best-effort：单次模型请求受 `REQUEST_TIMEOUT` 约束，可能超出剩余预算。这不是静默失败 —— lease 不会被确认，Worker 会在 lease 过期后重新派发。

所有执行任务的 goroutine（定时批处理 worker、人工任务 worker、调度循环、HTTP 服务）都有 `recover` 保护。裸 goroutine 里的 panic 会终止整个进程并连带 HTTP 服务，在 `restart: unless-stopped` 下变成崩溃循环。恢复后 panic 仍会作为批次错误上报并带上堆栈，因此进程存活的同时失败依然可归因、可见。

## 健康模型

存活与就绪严格分离。`/healthz` 只表示进程存在，永远返回 `200`，因此 Worker 或模型临时不可达时不会触发重启循环，进程有机会自行恢复。`/readyz` 反映实际可服务状态：启动前置检查未完成或被标记为降级时返回 `503`，`ready_reason` 和 `unhealthy_since` 说明未就绪的原因和起始时间。

启动前置检查包括词表加载和一次模型契约自检，两者都在 HTTP 监听开始之前完成。配置错误因此表现为启动失败，而运行期故障表现为 `/readyz` 503。批次失败本身不会让服务变为未就绪，但明确的配置或上游契约错误（`400/401/403/404`）会把跟踪器标记为降级，需要一次成功的启动级恢复才能清除，以免在配置错误时继续消耗重试次数。

## 组件

| 包 | 责任 |
| --- | --- |
| `internal/cairn` | 调用 Worker 内部队列 API |
| `internal/enrich` | xAI Responses 协议、Eino 工作流、严格输出校验 |
| `internal/taxonomy` | 版本化词表、别名归一化、分类枚举和输出校验 |
| `internal/processor` | 有界并发、批处理和失败上报 |
| `internal/health` | liveness、readiness 和最近一批状态 |
| `internal/dashboard` | 中文收藏列表、独立阅读页、同源查询/图片代理和有界人工处理队列 |
| `internal/config` | 环境变量解析及启动时校验 |

人工任务先按 ID 在 Worker 原子领取，再进入本机有界队列。定时任务与人工任务最终都通过 `processor.Process` 的共享信号量，因此总模型并发不会超过 `MAX_CONCURRENCY`。

## LLM 契约

请求只使用 `POST /responses`，搜索路径强制 `tool_choice=required` 和 `tools=[{"type":"x_search"}]`。strict JSON Schema 要求模型一次返回 `ai_title`、`original_language`、`original_text`、`translated_text`、`summary`、`related_links`、`image_urls` 和 `classification`；提示词明确原文保持原始语言、译文使用简体中文，标题约 20 个简体中文字符。

词表由 Worker 的 `src/taxonomy.json` 统一管理，Go 在启动时读取并验证。所有请求都附词表和枚举，Eino 的校验节点将别名映射为稳定 ID，丢弃未知/停用标签并标记待确认。分类信息不足不会丢弃已经验证的原文和摘要。

人工分类单独写入 `curation`，检索优先于模型 `classification`；人工收藏原因 `why` 和整理状态 `curation_status` 不随模型重跑覆盖。整理状态与 lease/重试状态互不混用。详细数据契约及升级顺序见 [收藏管理说明](bookmark-management.md)。

适配器白名单识别官方 `x_search_call`，同时兼容目标端点实测返回的 `x_thread_fetch`、`x_keyword_search`、`x_semantic_search`、`x_user_search` 自定义调用。没有搜索证据、没有且仅有一个输出块、结构不合法、标题不是合理长度的中文或 URL 不安全时，任务失败而不写入结果。

图片 URL 仅允许 `pbs.twimg.com/media`。模型结果通过校验后，Go 服务先让 Worker 在当前 lease 下抓取图片并写入 R2，再把 R2 对象引用随文本结果提交到 D1。Worker 完成事务前会确认引用对象存在；页面只能通过 Go 服务的同源 `/api/images/{key...}` 代理读取图片。

当 Worker 中的失败/耗尽记录已经有 `original_text`，或者后台人工提交了原帖正文，服务会跳过 `x_search`，只把可信原文交给模型补齐标题、语言、译文和摘要。这个路径仍使用同一套 strict JSON Schema 和结果校验，但不要求模型返回搜索证据，也不会接受模型新生成的图片 URL。
