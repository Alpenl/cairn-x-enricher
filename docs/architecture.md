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
| `internal/enrich` | xAI Responses 协议、顺序富化流程、严格输出校验 |
| `internal/classify` | Jev typed judgments、分类策略与原始概率审计 |
| `internal/taxonomy` | 版本化词表、别名归一化、分类枚举和输出校验 |
| `internal/processor` | 有界并发、批处理和失败上报 |
| `internal/health` | liveness、readiness 和最近一批状态 |
| `internal/dashboard` | 中文收藏列表、独立阅读页、同源查询/图片代理和有界人工处理队列 |
| `internal/config` | 环境变量解析及启动时校验 |

人工任务先按 ID 在 Worker 原子领取，再进入本机有界队列。定时与人工获取/阅读增强共享 `MAX_CONCURRENCY` 信号量。Jev 使用额外的一个串行分类 worker，与获取队列并行，拥有独立 lease、重试和输入版本。

## LLM 契约

获取请求使用 `POST /responses`，强制 `tool_choice=required` 和 `x_search`，仅返回原文、语言、独立上下文、链接与图片。原文快照立即保存。阅读增强通过第二个无搜索请求生成标题、译文与摘要，不含分类字段，且不能修改存档原文。

词表由 Worker 的 `src/taxonomy.json` 统一管理，Go 在启动时读取并验证。分类请求通过 TypeSafe `/v1/systemone` 发送，只有 Jev 问题携带词表定义。每个主题独立使用 Noul，形态/用途使用 Choice。原始概率和策略版本与分类一起保存；不确定结果可以留空并待确认。分类故障不改变已经获取的正文或阅读增强状态。

流程直接调用获取、存档、阅读增强和独立分类接口，没有运行时图构建。旧单次生成器仅用于复现实验。详细状态机、初始阈值、审计与回填方式见 [Jev 分类说明](jev-classification.md)。

普通网页列表使用 `view=summary`，详情和全文检索保留完整正文。Markdown 导出会补齐未加载的正文，失败时终止导出。阅读页只在展开时创建原文段落，未变化的轮询结果保留正文和图片节点。配套 Android 使用 Worker 的 `include=enrichment` 列表/详情协议、共享词表和人工整理接口。

人工分类单独写入 `curation`，检索优先于模型 `classification`；人工收藏原因 `why` 和整理状态 `curation_status` 不随模型重跑覆盖。整理状态与 lease/重试状态互不混用。详细数据契约及升级顺序见 [收藏管理说明](bookmark-management.md)。

适配器识别官方 `x_search_call`，兼容已有的 X 自定义搜索调用。获取缺少搜索证据、单一输出块、有效正文或安全 URL 时拒绝存档。标题/译文/摘要校验属于阅读增强阶段；这些校验失败时，已存档原文保留。

图片 URL 仅允许 `pbs.twimg.com/media`。原文存档后，Go 让 Worker 在获取 lease 下抓取图片写入 R2，再把对象引用随阅读增强结果提交。图片失败也不撤销原文；重试可复用快照中的媒体地址。Worker 确认对象存在后保存引用，浏览器仍通过同源代理读取。

已有快照的普通重跑复用原文；历史已有正文在首次重新处理时收纳为 `legacy_saved` 快照。人工正文标为 `manual`。两种来源都不重新执行 X Search，也不宣称做过新的来源验证。人工替换原文会失效旧分类租约并排队重新分类。
