# Issue #20 顶部历史进展快照

以下内容从 [原 issue #20](https://github.com/Alpenl/cairn-x-enricher/issues/20) 顶部逐字归档；任务正文与复选框仍留在 issue。

<!-- cairn-20260929-provider-ledger:start -->
### 付费账本与每日额度 · 2026-09-29

[Share `fa7fabd`](https://github.com/Alpenl/cairn-share/commit/fa7fabd7d023a4415592da452be06cc7bc03c83a) 用无私人身份的每日汇总行计数，删除收藏不返还全局额度；领取/预留不再扫描当日凭证。Worker 351/351、类型检查和 dry-run 通过。第 0 组仍须同负载测 SQL、rows_read/rows_written、p50/p95/p99、拒绝和未决积压，另测 off/basic/diagnostic 开销；业务账本始终开启。
<!-- cairn-20260929-provider-ledger:end -->

<!-- cairn-20260928-paid-baseline-scope:start -->
### 第 0 组口径补充：启动自检与实际网络尝试 · 2026-09-28

[Enricher `4045422`](https://github.com/Alpenl/cairn-x-enricher/commit/4045422) 修复了 Go 对 xAI POST 可能发生的传输层隐式重试；此前一次 `httpClient.Do` 不能直接当成一次供应商网络尝试。基线与同负载复测要用供应商夹具按实际收到的 POST 计数，并与持久账本逐项对照。分别统计启动 canary、来源首尝试、来源降级和阅读：网络次数、预留/未知数量、可得实际成本、重启频率及阶段延迟。每条收藏付费次数目标仍按收藏计算，启动 canary 另列每日总成本与配额耗尽率；日志 off 时业务账本仍计入 D1 写入和服务延迟。#11/#28 的账本合同未完成前，不把“没有重复请求”视为性能或预算目标达成。原复选框不变。
<!-- cairn-20260928-paid-baseline-scope:end -->

<!-- cairn-20260928-control-audit:start -->
### OBS-03：关闭日志时的最小审计已实现（Go 局部）· 2026-09-28

[Enricher `696cec2`](https://github.com/Alpenl/cairn-x-enricher/commit/696cec25082e392d74ac0edd45409c58937881ef) 加入独立于应用日志的控制审计，记录版本、模式、到期、来源类别、时间及 `started`/最终结果；私有 0600 文件按 14 天、4,096 项、256 KiB 收缩，并显式标记历史缺口。业务请求没有新增 DB/网络观测操作，只有管理员修改时写文件和每小时一次清理；审计坏文件保持业务运行并使控制入口只读。本地 `make verify` 通过（vet、lint 0 问题、全量 race、457/457 前端检查、构建）；[固定 HEAD CI](https://github.com/Alpenl/cairn-x-enricher/actions/runs/36440336904) 通过。

第 0 组仍须实测开关延迟、磁盘写入和 off/basic/diagnostic 的同负载开销；Worker/Web/Android、采集器和真实观察期仍未验收。原复选框不变。
<!-- cairn-20260928-control-audit:end -->

<!-- cairn-20260928-worker-observe:start -->
### OBS-03 控制面开销与后续度量 · 2026-09-28

[Share `e857f2a`](https://github.com/Alpenl/cairn-share/commit/e857f2aef495ec3298dc23a264158215ef47e4a7) 的 Worker 首次业务请求读 `0038` 单行配置、健康时缓存最多 30 秒，D1 故障时 5 秒失败退避；关闭应用日志不输出请求事件，但这条低频控制读取仍须计入 off 模式的 D1/延迟基线。Go [配套 `e487310`](https://github.com/Alpenl/cairn-x-enricher/commit/e487310e3bc6df057b4b39496a621ea239e84bd9) 每 5 秒检查本地版本，仅变化/失联时发布，确认后每 10 分钟重验。Worker basic/diagnostic 输出按 isolate 每分钟上限 120/600 条，丢弃统计仅在后续可记录事件出现时输出，不能据此推断平台零丢失。按第 0 组对 off/basic/diagnostic 做相同负载的 SQL 次数、rows_read、p95、日志字节和丢弃量比较，再确定采样及容量；当前无该测量，性能目标未签收。固定 HEAD [Share CI](https://github.com/Alpenl/cairn-share/actions/runs/36442943434) / [Go CI](https://github.com/Alpenl/cairn-x-enricher/actions/runs/36442971033) 通过。
<!-- cairn-20260928-worker-observe:end -->

<!-- cairn-20260928-completion-replay:start -->
### 阅读完成回执的 SQL 成本纳入基线 · 2026-09-28

[Share `904a08b`](https://github.com/Alpenl/cairn-share/commit/904a08b50f5db16f2c790ab7a0803d239cf844c3) 为每次阅读完成增加一次按租约摘要索引的回执读取及同事务的回执插入/业务更新；[Go `a533a93`](https://github.com/Alpenl/cairn-x-enricher/commit/a533a933533a5230069e750f29a81861c2103bce) 仅在提交网络故障或 5xx 时重发一次相同请求体，不增加模型调用。按第 0 组在相同完成率、响应丢失率与并发下测 SQL 数量、rows_read/written、完成 p95 和 Worker 内存；当前测试证明语义和有界请求次数，不冒充性能测量。`0039` 本地实际迁移及固定 SHA [Share CI](https://github.com/Alpenl/cairn-share/actions/runs/36445239795) / [Go CI](https://github.com/Alpenl/cairn-x-enricher/actions/runs/36445238208) 通过；远端未迁移/部署。
<!-- cairn-20260928-completion-replay:end -->



<!-- cairn-20260928-run-retention:start -->
### 0037 的性能计量要求 · 2026-09-28

[Share `a675c53`](https://github.com/Alpenl/cairn-share/commit/a675c533859566e7ed6092ae93c175d92baf818b) 给存活收藏的旧 run 加每轮 100 条扫描上限、年龄索引、复用依赖索引、墓碑回执和触发器；本地 Worker 29 文件、329/329 测试（`--maxWorkers=4`）、类型检查和部署 dry-run 通过；Wrangler 在临时本地 D1 实际应用到 0037 并经 HTTP 验证；Go↔Worker 多 run/丢响应场景通过，未增加模型调用；[Share CI](https://github.com/Alpenl/cairn-share/actions/runs/36437713682) 的 Worker 与 Android 均通过。

第 0 组基线须单列 0037 的每次 run 写入触发器成本、回填与压缩各轮 SQL/rows_read/rows_written/延迟/内存、被引用 run 占比、墓碑和完整 runs 列表随时间增长，以及按实际写入速率消化积压的能力。正确性测试不代表性能目标已达到；原复选框不变。
<!-- cairn-20260928-run-retention:end -->

<!-- cairn-20260928-live-retention:start -->
### 存活历史清理的 D1 计量项 · 2026-09-28

[Share `c7d0c18`](https://github.com/Alpenl/cairn-share/commit/c7d0c186450add6713965b269348a5ab78b6aaf0) 加入默认 90 天的有界历史清理：每次 Cron 各扫描最多 100 条旧事件/快照，并用 0036 的年龄与引用索引保护当前内容及仍被使用的历史。本地 Worker 29 文件、324 项测试（`--maxWorkers=4`）、类型检查及部署 dry-run 通过；0036 经本地 Wrangler 迁移实际应用；[Share CI](https://github.com/Alpenl/cairn-share/actions/runs/36434554270) 的 Worker 与 Android 均通过。

第 0 组性能报告要单列 0036 的索引写入成本、每轮 SQL/rows_read/rows_written/耗时、在大量被引用旧快照前的游标推进，以及积压消化速度；这批正确性测试没有证明性能目标。原复选框不变。
<!-- cairn-20260928-live-retention:end -->

<!-- cairn-20260928-projection-replay:start -->
### 整份选择回执的性能计量边界 · 2026-09-28

[Share `2fd8361`](https://github.com/Alpenl/cairn-share/commit/2fd83617ad3ede2bd97468b51360dd743b2ec376) 为显式 `operation_key` 的 v1/v2 整份选择增加一次回执查询与一条事务内回执 INSERT；解决后续编辑后重试按新状态重算的错误，并保持原子投影。Worker 28 文件、319 项测试、类型检查、部署 dry-run 通过；本地 Wrangler 实际应用 0035 迁移并经 HTTP 验证首次确认→后续修订→同 key 原回执、异 payload 409；Go↔Worker/D1 多 run 决定场景通过。[Share CI](https://github.com/Alpenl/cairn-share/actions/runs/36432587219) 的 Worker、Android 均通过。

第 0 组基线应把这次 D1 读写、同 key 重试时的投影修复和无动作请求计入开放/混合负载，仍须实测 rows_read/written、延迟与长期保留成本；不能把功能测试当成 D2 性能目标通过。原复选框不变。
<!-- cairn-20260928-projection-replay:end -->

<!-- cairn-20260928-obs-async:start -->
### Go 日志导出与安全字段局部交付 · 2026-09-28

[`9cf2566`](https://github.com/Alpenl/cairn-x-enricher/commit/9cf2566c28800b32e6ee26d34974c89bc6462732) 接在已有运行时开关之后：1,024 条有界异步队列、满时丢诊断并计数、写入失败计数、最长 2 秒排空。Go 字段和值在排队前按白名单过滤，错误保留安全类别/HTTP 状态；阻塞与失败 writer、敏感 canary、热切换均有 race 回归。完整 `make verify` 通过，[当前 CI](https://github.com/Alpenl/cairn-x-enricher/actions/runs/36425852188) 已通过。该 Go 局部不等于 OBS-04 全部：采集器、批量与有限重试、私有保留/删除、全端诊断和同负载 off/basic/diagnostic 开销仍未验收；OBS-02/03、持久付费账本和 14 天实际使用报告也继续开放。原复选框不变。
<!-- cairn-20260928-obs-async:end -->

<!-- cairn-20260928-paid-guard:start -->
### 付费边界与性能报告补充 · 2026-09-28

[E `1caf2c7`](https://github.com/Alpenl/cairn-x-enricher/pull/17)、[S `6840b29`](https://github.com/Alpenl/cairn-share/pull/32) 加入结果未知护栏。第 0 组同负载报告须分别统计实际供应商网络尝试、已确认成功、失败、结果未知、可得用量与可能计费次数；把阻塞队列数量/最老年龄、人工 `provider_result_unknown` 拒绝率、额外租约准入/D1 写入与端到端延迟一并计入，不能以“没重复请求”掩盖永久积压。日志可关闭时，业务账本和这些安全状态仍须存在；详细事件开关的吞吐、内存和导出丢失另测。

**升级顺序更正：**原 P5 段“先升级 Worker”只适用于旧 Go 已停止并排空在途租约之后；旧 Go 的同一租约内 HTTP 重试仍可能多次付费，新 Worker 的租约标记无法拦截它直连供应商。批准部署后应先停旧 Go、排空或保守登记在途，再迁移 Worker、启动新 Go，回滚不得自动清除未知标记。当前无生产部署、迁移或付费实验；#11 的逐次账本/对账与本 Issue 性能目标均未完成，原复选框不变。
<!-- cairn-20260928-paid-guard:end -->

<!-- cairn-20260928-implementation:start -->
### 性能改造实施快照 · 2026-09-28

人工请求持久化及共用调度领取已进入 [Enricher Draft PR #17](https://github.com/Alpenl/cairn-x-enricher/pull/17) `07b2346` 与 [Share Draft PR #32](https://github.com/Alpenl/cairn-share/pull/32) `7dff44d`；Go 人工入口不再持有等待执行的租约。优先级领取 SQL 在 2,000 条本地 D1 样本上的 `rows_read` 为 1。v2 备注编辑导致的无效重分类已由 Share `26d982f` 修复。

来源付费阶段新增租约准入，短租约放回队列，未开始付费的新版租约退还 attempt；滚动升级前先握手。每个付费阶段增加一次 Worker 请求与 D1 条件更新，其延迟和读写开销须进入第 0 组同负载基线报告。

P1 已在 Draft PR #17 实现：来源、分类、补证据恢复各自调度，来源满批后立即接下一批，直到队列为空、组件故障或关停，且单次领取限时；慢来源与慢证据网络不阻塞分类半开探测，单条内容失败不截断来源本轮。`once`/`classify` 保留剩余额度内的恢复后重新分类。本地 `make verify`、真实 Go↔Worker HTTP/D1 补证据恢复联调通过；[当前 CI](https://github.com/Alpenl/cairn-x-enricher/actions/runs/36416368537) 绑定 `07b2346`。尚无同负载吞吐/延迟前后报告，预期收益不能当作实测。供应商未知结果账本、OBS-01–05 与完整性能目标仍待完成；原复选框不变。
### Go 日志热开关局部交付 · 2026-09-28

[Draft PR #17](https://github.com/Alpenl/cairn-x-enricher/pull/17) `5437651` 实现 Go 端持久日志开关、容器回环 CLI 与 LAN 只读状态。off/basic/diagnostic 可运行时切换，临时诊断到期恢复进入前模式；[远端 CI](https://github.com/Alpenl/cairn-x-enricher/actions/runs/36418986737)、本地 `make verify` 和 Compose 解析通过。上述是 `5437651` 当时的状态；后续 `9cf2566` 已补 Go 有界异步导出与字段/错误过滤。同负载开销、OBS-02 事件/指标/链路、Worker/Web/Android 传播及 OBS-04 其余验收仍未完成。生产 v0.6.0 未更动，本轮性能目标与原复选框不因这个局部交付改变。
<!-- cairn-20260928-implementation:end -->
