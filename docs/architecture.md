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

`SHUTDOWN_TIMEOUT` 是**整个**优雅关停的预算，不是定时批次的执行窗口：HTTP 停止接受连接与等待在途任务共用同一份 deadline。来源和分类分别调度；每次领取调用单独限时，已领取的任务继续受各自阶段时限约束。来源满批且仍有工作时立即接下一批，收到关停信号后停止新领取。两个 compose 文件把 `stop_grace_period` 设为 30s，必须大于 `SHUTDOWN_TIMEOUT`。

排空是 best-effort：单次模型请求受 `REQUEST_TIMEOUT` 约束，可能超出剩余预算。超时退出时未确认的 lease 由 Worker 在过期后重新派发；供应商结果未知的持久调用账本与跨重启恢复上限仍在 #11 待完成，不能仅凭 lease 重发保证不重复付费。

来源存档和阅读完成提交在网络故障或 Worker 5xx 后只重发一次相同请求体，不再发起模型调用。来源写入使用相同租约与内容，重复存档不增加内容版本；阅读完成由 Worker 的 `0039` 回执在同一 D1 事务中提交，已完成的相同租约与内容返回原回执，不同内容返回冲突。回执只保存租约和载荷的摘要、收藏 ID、完成时间；删除收藏会级联删除。新 Go 启动时要求 Worker 声明 `completion_replay` 能力，因此发布顺序为先迁移/部署 Worker，再升级 Go。这只解决提交响应丢失；供应商请求本身没有响应时，仍需 #11 的逐次付费账本与有权限的恢复决策。

所有执行任务的 goroutine（定时批处理 worker、人工任务 worker、调度循环、HTTP 服务）都有 `recover` 保护。裸 goroutine 里的 panic 会终止整个进程并连带 HTTP 服务，在 `restart: unless-stopped` 下变成崩溃循环。恢复后 panic 仍会作为批次错误上报并带上堆栈，因此进程存活的同时失败依然可归因、可见。

## 健康模型

存活与就绪严格分离。`/healthz` 只表示进程存在，永远返回 `200`，因此 Worker 或模型临时不可达时不会触发重启循环，进程有机会自行恢复。`/readyz` 反映实际可服务状态：启动前置检查未完成或被标记为降级时返回 `503`，`ready_reason` 和 `unhealthy_since` 说明未就绪的原因和起始时间。

启动前置检查包括词表加载和一次模型契约自检，两者都在 HTTP 监听开始之前完成。配置错误因此表现为启动失败，而运行期故障表现为 `/readyz` 503。明确的配置或上游契约错误会让对应的来源或分类组件降级；分类熔断在退避到期后只用一条可领取任务探测，成功后只清除分类故障。来源与分类的最近一轮结果分别保存在健康状态中。

## 本地日志热开关

新版本可用 `deploy/nas/compose.observability.yaml` 叠加持久配置卷；当前生产 Compose 仍钉在 v0.6.0，不应用该叠加文件。控制 HTTP 只监听容器内 `127.0.0.1:9090`，不向宿主机或局域网发布。管理员经 SSH 登录 NAS 后执行 `docker exec cairn-x-enricher /cairn-x-enricher observe show` 读取期望版本及实际模式，再用 `observe set-log --mode off|basic|diagnostic --expected-version <版本>` 修改；`diagnostic` 默认 15 分钟，最长 1 小时，到期自动回到进入诊断前的 off 或 basic。配置先写入卷再生效，响应丢失时先重新读取版本。`LOG_LEVEL` 是 basic 的最低日志级别；全局 off 关闭应用 JSON 日志。

启用观测配置时，Go 日志经 1,024 条有界队列异步写入 stderr；输出阻塞时业务请求不等待，超额日志丢弃并累计计数。只读状态的 `log_exporter` 报告队列占用、丢弃与写入错误；关停最多等待 2 秒排空，避免采集端故障拖住退出。导出前按固定字段和值白名单过滤：原文、prompt、URL、异常详情、panic 和栈不会进入可选日志，错误仅保留固定类别与 HTTP 状态。此队列只负责日志传输，不能代替付费尝试的持久账本。

同一版本的日志模式由 Go 每 5 秒检查并通过独立 Enricher Token 发布到 Worker；启动、重启和失联恢复后也会重发。Worker 只接受版本递增或同版本同内容重放。`observe show` 的 `worker_publish_state` 区分 pending、confirmed、conflict、rejected 和 unavailable，`worker_persisted_version` 是最近一次持久确认，`worker_last_confirmed_at` 是该确认时间；它们不代表全部 Worker isolate 已刷新。Go 每 10 分钟重验一次已确认版本。Worker 业务响应的 `X-Cairn-Observability-Version` 报告处理该请求时的实际配置版本，读取配置失败时另报 `X-Cairn-Observability-Status: unavailable` 并关闭应用诊断。Worker isolate 在首次业务请求读取配置，随后最多缓存 30 秒；诊断到期在本地自动回落。配置读取属于有界控制面开销，不会给每个请求增加 D1 查询。

目前跨端同步仅覆盖日志模式。Worker 应用日志只写固定路由模板、状态和耗时，不写原始路径、查询串或私人 ID；响应字节和 D1 统计不可得时明确为 null/unavailable，每个 isolate 的日志数有分钟上限。平台 `wrangler.jsonc` 的 observability 配置独立于应用开关。指标、链路、完整业务事件、私有采集器和观察期报告仍按 #20 OBS-01–05 实施，状态接口明确标为不可用。

开关动作另存于同卷的 `observability.json.audit.json`（权限 0600），不经过可关闭的应用日志。管理员可用 `observe audit` 在容器回环控制端口读取：每次有效配置变更先写 `started`，再原子保存配置，最后写 `applied`、`rejected` 或 `unconfirmed`。若进程在两次审计写入间退出，只有 `started`；应与 `observe show` 的持久版本对照，不能假称成功。审计只含时间、版本、信号、模式、诊断到期和操作者来源类别，不含收藏、任务、正文或凭据。审计最多保留 14 天、4,096 条、256 KiB；超出时在 `truncated_before_version` 标记版本缺口。服务每小时及重启时清理过期记录；停机期间的物理清理会延至下次启动。审计写入失败会阻止新的配置变更，已发生的写入错误计入只读状态 `control_audit_errors`；审计文件损坏时服务继续运行，但控制写入保持关闭，状态 `control_audit_available=false`，需修复文件并重启。旧配置文件格式未改变，回退旧版本仍能读取最新开关，但旧版本不会继续写审计。

## 组件

| 包 | 责任 |
| --- | --- |
| `internal/cairn` | 调用 Worker 内部队列 API、v2 域 API 与权威目标握手 |
| `internal/enrich` | xAI Responses 协议、独立 `ReadingResult`、typed 错误分类与有界退避 |
| `internal/classify` | typed 判断原语（Noul/Choice/Score）、问题编译器、证据准备、纯 `Decide`/`Resolve`/`Replay` |
| `internal/taxonomy` | 版本化词表、别名归一化、分类枚举和输出校验（v1 投影） |
| `internal/processor` | 有界并发、独立分类 worker、组件暂停与 stale 处理 |
| `internal/health` | liveness、readiness 和最近一批状态 |
| `internal/dashboard` | 中文收藏列表、阅读页、多维字段级整理、v2 代理与有界人工队列 |
| `internal/extension` | 有界语义扩展：实体候选/受限判断、受控外链抓取、重排、词表提案（默认关闭） |
| `internal/config` | 按命令角色解析与校验环境变量 |
| `internal/evaluation` | 共享数据集 schema、只读生产导出及离线评分/校准；服务主路径不执行评估 |
| `experiments/classification/main` | 显式离线评估 CLI，复用内部评估库 |

人工请求先在 Worker 持久化，系统空闲时通知来源调度器；领取发生在取得执行容量之后。粘贴原文在返回 accepted 前保存为来源快照。来源获取/阅读受 `MAX_CONCURRENCY` 约束；Jev 分类与补证据恢复各有独立调度循环，不等待来源批次结束。分类拥有独立 lease、预算、重试和输入版本。

## LLM 契约

获取请求使用 `POST /responses`，强制 `tool_choice=required` 和 `x_search`，仅返回原文、语言、独立上下文、链接与图片。原文快照立即保存。阅读增强通过第二个无搜索请求生成标题、译文与摘要，不含分类字段，且不能修改存档原文。

词表由 Worker 的 `src/taxonomy.json` 统一管理，Go 在启动时读取并验证。分类请求通过 TypeSafe `/v1/systemone` 发送，只有 Jev 问题携带词表定义。每个主题独立使用 Noul，形态/用途使用 Choice。原始概率和策略版本与分类一起保存；不确定结果可以留空并待确认。分类故障不改变已经获取的正文或阅读增强状态。

流程直接调用获取、存档、阅读增强和独立分类接口，没有运行时图构建。旧单次生成器仅用于复现实验。详细状态机、初始阈值、审计与回填方式见 [Jev 分类说明](jev-classification.md)。

普通网页列表使用 `view=summary`，详情和全文检索保留完整正文。Markdown 导出会补齐未加载的正文，失败时终止导出。阅读页只在展开时创建原文段落，未变化的轮询结果保留正文和图片节点。配套 Android 使用 Worker 的 `include=enrichment` 列表/详情协议、共享词表和人工整理接口。

人工分类单独写入 `curation`，检索优先于模型 `classification`；人工收藏原因 `why` 和整理状态 `curation_status` 不随模型重跑覆盖。整理状态与 lease/重试状态互不混用。详细数据契约及升级顺序见 [收藏管理说明](bookmark-management.md)。

适配器识别官方 `x_search_call`，兼容已有的 X 自定义搜索调用。获取缺少搜索证据、单一输出块、有效正文或安全 URL 时拒绝存档。标题/译文/摘要校验属于阅读增强阶段；这些校验失败时，已存档原文保留。

图片 URL 仅允许 `pbs.twimg.com/media`。原文存档后，Go 让 Worker 在获取 lease 下抓取图片写入 R2，再把对象引用随阅读增强结果提交。图片失败也不撤销原文；重试可复用快照中的媒体地址。Worker 确认对象存在后保存引用，浏览器仍通过同源代理读取。

已有快照的普通重跑复用原文；历史已有正文在首次重新处理时收纳为 `legacy_saved` 快照。人工正文标为 `manual`。两种来源都不重新执行 X Search，也不宣称做过新的来源验证。人工替换原文会失效旧分类租约并排队重新分类。
