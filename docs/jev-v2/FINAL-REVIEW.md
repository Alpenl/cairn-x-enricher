> 2026-09-23 B08 最新局部证据：[六维生产 policy 离线重放与拟合](evidence/B08-20260923-policy.md)。E `200b86e`；冻结规则后对 42 训练样本/7 组执行 336 个组合，0 候选满足逐维覆盖/错误率约束，未选 policy、未晋升。全仓本地与远端门禁通过；原响应元数据恢复和拟合均零新增调用，dev/holdout 未使用。下一步潜在用途/局部弃权训练诊断，完整原范围保留。

> 2026-09-23 B08 最新局部证据：[门槛与消融可比性](evidence/B08-20260923-gate.md)。E `45e755a`；22 个旧失败场景、完整 make verify 通过。非法 gate/报告和 train/dev 晋升拒绝，不同参考集不再直接相减；本批零付费，未读取冻结 dev/holdout，真实质量及完整 B08 仍待验收。

> 2026-09-23 B07 收藏库最新局部证据：[Android 服务端筛选与分页](evidence/B07-20260923-library.md)。运行时 S `76e3e66`、最终设备夹具 `8f1bb32`；无关键词筛选独立查询服务端集合，完整多维条件/第四主题不再受 v1 本地投影限制；确认动作重读成员，离线草稿不改变保存结果，分页时间固定、旧请求/账号和非法 cursor 有守卫。73 unit、本地 API26 29 普通 + 11 实际 Worker 阶段通过，远端结果见报告。完整原范围继续保留。

> 2026-09-23 B05 客户端前批局部证据：[Go/Web 多维筛选及显式确认](evidence/B05-20260923-client.md)。受测 S `37b1dce` / E `f091c45`；新维度与实体状态经 Go/Web 实际列表和导出传递，旧后端缺少确认可见拒绝。九个独立跨服务场景、40 项实际服务浏览器、57 项合同浏览器通过；205 Worker 与完整 Go 门禁通过。该批尚未修复的 Android 收藏库由顶部报告推进，完整原范围未完成。

> 2026-09-23 B05 前批局部证据：[实际多维筛选、实体状态、计数和缓存](evidence/B05-20260923-filter.md)。受测 S `aad7d0a`；两真实列表入口已接入权威有效值，同维 OR/跨维 AND，非法条件拒绝，计数按筛选条件，0025 修复来源变化缓存。完整原范围仍未完成，Android 无关键词收藏库的 v1 本地过滤另行修复；远端门禁与规模限制见报告。

> 2026-09-23 B07 最新局部证据：[搜索写入竞争与凭据持久化顺序](evidence/B07-20260923-search-session.md)。受测 S `176c1aa`；确认写入取消旧搜索与 cursor、重新判断筛选归属；凭据 revision 与串行写入阻止旧通知切回账号，并保留更新的外部设置；保存失败可见且可重试。70 Android unit、本地 API26 23 普通 + 10 实际 Worker 阶段通过；远端结果见报告。完整原范围未完成，实际 v2 筛选及其余原矩阵继续保留。前批 [重置与迁移](evidence/B07-20260922-reset.md)、[状态合同](evidence/B07-20260922-state.md) 证据继续有效。

> 2026-09-22 新增 B09 局部证据：[DNS 校验与实际套接字绑定、特殊地址拒绝](evidence/B09-20260922-dns.md)。最终 E `67659f7`，完整 Go 门禁与实际 TLS/处理器恢复通过；24 个旧地址绕过失败对照保留。普通外链策略保守拒绝转换/协议专用网段，完整 B09 和总目标仍待完成。本批 0 次付费。

> 2026-09-22 最新局部证据：[B07 账号会话、迟到响应与版本化阅读缓存](evidence/B07-20260922-cache.md)，[十条原任务审计](evidence/B07-20260922-scope-audit.md)。运行时 S `30110e7`，最终夹具 S `d217acd`；181 Worker / 65 Android unit / 8 个跨仓服务场景通过，最终 API26/35 各 17 普通 + 7 个真实 Worker 独立进程阶段通过。旧版三场景失败对照、API35 短暂提示竞态与修复复验均有原始记录。新增迁移 0023 尚未在生产执行。B07 全范围及 B08/B09 等仍待完成，本批 0 次付费，统一验收继续进入 #16。

> 2026-09-22 前批局部证据：[B07 完整账号绑定、旧队列恢复与独立自动基线](evidence/B07-20260922-account-baseline.md)，以及 [B07 十条原任务审计](evidence/B07-20260922-scope-audit.md)。受测 S `3a891f1` / E `86b82b5`；180 Worker、64 Android unit、8 个真实跨仓场景通过。最终代码 API26/35 各 13 普通设备 + 7 个独立真实 Worker 进程阶段通过，XML 和日志见该报告。设置入口、旧队列和 reset 的局部修复不代替状态/候选/读取缓存/实体导出/完整兼容的剩余工作。本批 0 次付费，整体验收继续进入 #16。

> 2026-09-22 前批局部证据：[R3-07 Android 持久动作链、丢响应和真实进程恢复](evidence/R3-20260922-android-recovery.md)。S `3aae256` / E `60e0497`；179 Worker、63 Android unit、11 常规设备测试及 3 个真实 Worker 设备阶段、8 个 Go 跨仓场景通过。该批登记的账号尾缀/旧队列/reset 缺口由顶部新报告继续修复；完整范围见新审计。该批 0 次付费。

> 2026-09-22 当前工程进展：[R3-06 补材料执行所有权、持久检查点与原子恢复](evidence/R3-20260922-escalation.md)。受测 S `babe6bb` / E `c470d90`；Worker 177 项、Go 完整检查、8 个真实跨仓场景及补材料真实 race 场景通过。本批 0 次付费调用；扩展仍默认 off，整体验收未完成。

> 2026-09-22 当前工程进展：[R3-12 决定事务 CAS、完整运行引用与精确重试]( evidence/R3-20260922-decisions.md)。受测 S `3b93f17` / E `9fe17ab`；Worker 166 项、Go 完整检查及 7 个真实跨仓场景通过，本批 0 次付费调用。整体验收仍待完成。

> 2026-09-22 当前工程进展：[生产存读后的问题级复用、调用来源与历史输入身份](evidence/R3-20260922-reuse.md)。受测 S `dcfd612` / E `d606002`；Worker 148 项、Go 完整检查、6 个真实跨仓场景通过，本批 0 次付费调用。自动参考训练诊断与整体验收仍按各自范围记录，下文历史证据保留。

> 2026-09-22 最新 B08 局部证据：[240 条冻结自动参考、42 条真实训练与零网络恢复](evidence/B08-20260922-automatic-reference.md)。E 受测代码 `ac60d9a34968e3e2fc2087305f6332f038116f92`；43 次调用含烟雾重复，训练 gate 因仅 7 组而 inconclusive，dev/holdout 未调用。全目标仍待完成。

> 2026-09-22 update: [R3-03 provenance, migration and real browser evidence](evidence/R3-20260922-provenance.md). Full acceptance remains pending.

Latest scoped entity evidence: [R3-09 snapshot identity, real processor, UI and export](evidence/R3-20260922-entities.md). Worker 145 tests, five real integration scenarios and 33 browser checks pass; complete acceptance remains pending.

> 2026-09-22 update: [R3-05 actual request budget evidence](evidence/R3-20260922-budget.md). Full acceptance remains pending.

> 2026-09-22 update: [R3-02 snapshot identity and real recovery evidence](evidence/R3-20260922-evidence.md). Full acceptance remains pending.

> 2026-09-22 update: [R3-04 single-choice/browser and R3-11 container evidence](evidence/R3-20260922-curation.md). Tested Share `76b1705d764416e95a93fed87e38966e30c21772`, Enricher `e85632028ad70aa5adb64ba6d9fd5c4f4798c94b`; full acceptance remains pending.

# FINAL-REVIEW：Cairn Jev v2 跨仓复审证据

## 2026-09-22 首批 R3 历史证据快照（当前结果见顶部）

总控 [#10](https://github.com/Alpenl/cairn-x-enricher/issues/10)，最终验收 [#16](https://github.com/Alpenl/cairn-x-enricher/issues/16)。整体仍 `in_progress / acceptance_blocked`；本轮局部修复不等于 126 任务整体验收。

| 仓库 | 本轮受测代码 SHA | 实现 PR |
|---|---|---|
| Share | `3c59f547d0c3a1acf5b5959dd16383709d6cf33d` | [#32](https://github.com/Alpenl/cairn-share/pull/32) |
| Enricher | `1836179d486bc018ba5c890d507511b7f56bf2d4` | [#17](https://github.com/Alpenl/cairn-x-enricher/pull/17) |

详见 [R3 第一批代码、失败回归、完整日志与限制](evidence/R3-20260922-runtime.md)。本报告是上述代码后的纯文档变更；代码版本与报告版本分别记录。

| 验证范围 | 本轮实际结果 |
|---|---|
| Worker 本地真实 D1 / unit / contract | 129/129；包括 R3-01 最后 preflight 后的 target/content/lease 竞争、不同 operation 并发、SQL 回滚与幂等 |
| Worker typecheck / deploy:dry-run | 通过；未部署 |
| Enricher make verify | vet、完整 lint、全包 race、前端 73/73、build 通过；原 context 参数顺序 lint 已修 |
| Go ↔ 真实本地 Worker/D1/R2 | 三个用例通过；真实提交丢响应后 2 次相同 operation 提交、1 次外部模型 fixture 调用、1 个 run |
| 半开恢复 | 临时错误、取消、零任务、空队列与成功六种路径；并发探测、递增退避和 race 回归通过 |
| 浏览器 / Android 模拟器 / 真机 | 本轮未运行；旧阶段成绩保留在历史中，不外推为本轮全链验收 |
| Live / 模型质量 | 本轮未运行；按所有者更新采用自动参考基准，详见 [授权记录](AUTHORIZATION-20260922.md) |
| GitHub CI | 本地门禁不代替远端结果；推送后按最新 PR HEAD 核对并登记于 #16 |

R3-01/08 已有上述局部修复和回归证据；R3-11 代码部分完成，本轮整体证据仍待后续汇总。R3-02/03/04/05/06/07/09/10/12 仍待修复及验证；原 R/B/SC 不因未列出而取消。工程、自动基准质量、独立复审、合并、部署分别判断。

## 历史记录

下方原文来自报告提交 `1fd1eb72643343dc8601322c98a5b8ee423c6fcd`。其中早期固定 spec、不同阶段 SHA、设备“未运行/已运行”和部分整体 pass 均只作历史交付声明，不代表当前代码已验收。后续 R3 已指出的缺陷及重新验证要求优先；历史日志和真实局部进展继续保留。

<details>
<summary>展开 2026-09-21 历史报告（非当前验收结论）</summary>

# FINAL-REVIEW：Cairn Jev v2 跨仓库最终复审证据（2026-09-21 修复轮）

日期：2026-09-21
总控：[E #10](https://github.com/Alpenl/cairn-x-enricher/issues/10)
最终验收：[E #16](https://github.com/Alpenl/cairn-x-enricher/issues/16)

> 本文是**证据快照**，不是发布授权，也不代表 126 任务或 SC01–SC30 已整体验收。
> Issue 是实时状态的唯一来源。上一版报告（2026-09-20，E `4031200`/S `8f98ac0`）保留在 git
> 历史中；其超出测试范围的整体“通过”结论已在本版逐项撤回。

## 1. 实际 SHA

| 仓库 | 上一轮审查基线 | 本轮测试代码 SHA | 分支 / PR |
| --- | --- | --- | --- |
| cairn-x-enricher (E) | `2c00b7546187594d30424e0b340eb8da4258a744` | `1462fb4` | `impl/jev-v2-fixes-20260921` / E PR #17（Draft） |
| cairn-share (S) | `3520c81695cfd17274982877bd0d404581662fc8` | `4632e86` | `impl/jev-v2-fixes-20260921` / S PR #32（Draft） |

R2 复审修复轮：E `1462fb4`（测试代码）、S `4632e86`；本报告的文档提交为
`git log -1 --format=%H -- docs/jev-v2/FINAL-REVIEW.md`。R1 的 F01–F14 为 E `faad44c`/S `4c03094`，
B05/B06/B07/B09 工程为 E `a85b870`/S `ccd7080`，SC26/分批/B08 为 E `6abc76d`，设备修复为 S `e08d41f`。

第三轮（SC26 问题级重用、B04-T06 有界分批、B08 生产导出、Android 设备验证）的提交：
E `6abc76d`、S `e08d41f`；第二轮（B05/B06/B07/B09 工程）为 E `a85b870`、S `ccd7080`；
第一轮 F01–F14 为 E `faad44c`、S `4c03094`。

本报告本身是后续纯文档提交：报告引入提交 `5f735b6db13c1ebde75024c61b6d7fc36fe08e23`，
第二轮更新提交以 `git log -1 --format=%H -- docs/jev-v2/FINAL-REVIEW.md` 为准。
测试代码 SHA 与报告 SHA 分开列出，不把早期 head 当最新成绩。

Schema/spec/model/policy 版本：

- Worker 迁移：`0013_decision_history.sql`、`0014_content_revision_guard.sql`（新增，0009 字节未改）。
- 分类 spec：生产 `classify-v1`（score 关闭）；启用 Score 时为 `classify-v1-score`，语义 hash 由
  Go/TS 共享算法计算。
- Policy：`jev-policy-v2`（未校准，`calibrated:false`）。
- Feature flags：`entities`/`evidence`/`rerank`/`proposal` 全部**默认 off**。

## 2. F01–F14 逐项处理与证据

| 发现 | 处理 | 实现位置 | 验证（命令/断言） | 状态 |
| --- | --- | --- | --- | --- |
| F01 | 内部 QuestionSpec 与官方 provider DTO 分离；questions 为 map、使用 `type`、结构化 instructions/criteria；Score 浮点位置 + 索引 legend + 有序概率 | E `internal/classify/{jev,questions,primitives,policy}.go` | `contractServer` 拒绝旧数组/`kind`；`TestProviderRequestMatchesOfficialFixture`；`TestScoreUsesOfficialFloatLegendContract`；官方文档 2026-09-21 核实 | engineering_done（contract） |
| F02 | Worker v2 claim 从服务器 target 绑定 spec/taxonomy/policy/model + 原子目标指针守卫；Go 使用编译出的 SpecID 声明能力 | S `classification.ts`；E `cairn/stages.go` | S 回归（修复前失败：绑定 "undefined"/complete 0 行；修复后 14/14）；真实 Go→Worker/D1 claim→complete（local integration） | engineering_done（contract + real_local） |
| F03 | V2SelectionView 覆盖真实 fallback/v2 两种响应；冲突透传 revision，不折成 502 | E `cairn/v2.go`、`dashboard.go`；S `taxonomy-routes.ts` | `TestSelectionDecodesBothRealWorkerShapes`、`TestConflictRevisionIsForwarded`；真实 Worker 响应经实际 Go client/代理 | engineering_done |
| F04 | 四维 override 与操作语义统一；selection/effective/投影同一结果；revision_conflict 安全透传 | S `domain.ts`、`domain-routes.ts`、`taxonomy-routes.ts`；E proxy/UI | S `test/domain.test.ts`（四维操作→刷新一致）；浏览器断言；真实集成 selection/effective | engineering_done |
| F05 | 生产链路：v2 词表→不可变 spec→evidence→真实 Evaluate→run/decision 同事务→有效视图/v1 投影；第四主题保留 | E `main.go`、`processor/stages.go`、`classify`；S complete handler | `tests/local-integration/run.sh` 从新收藏生成 run/decision（无手工 seed）；断言 effective projected=true | engineering_done（real_local） |
| F06 | RawAnswer 编解码互逆；runs/spec 结构化读取；重放加载真实 spec + 历史 policy；缺 policy 显式失败；`--commit` 需 `--authorize-write` | E `classify/primitives.go`、`resolve.go`、`replay.go`；S `domain-routes.ts` | `TestRawAnswerRoundTripsThroughProviderShape`、`TestReplayRefusesDefaultPolicySubstitution`、`selectReplayRun`；真实 Evaluate→存→读→Replay | engineering_done |
| F07 | decision 校验 run 归属/spec/model/coverage/内容 revision；服务端重放 override；过期 run 拒绝 | S `domain-routes.ts` | `test/domain.test.ts`：unknown/foreign/model mismatch/run_stale；人工 reject 在 replay 后保留（真实集成） | engineering_done |
| F08 | 快照只追加（同身份异 bytes 冲突）；override CAS/事件/revision 同事务；source/URL 变化推进 content revision | S `domain-routes.ts`、migrations 0013/0014 | S 并发断言：两个同 revision 请求恰好一个 200 一个 409、revision 只 +1、事件/override 各 1；快照 bytes 不变 | engineering_done |
| F09 | 熔断跨 poll：claim 前检查；暂停期间不领取；退避后单次探测；当前 lease 留待过期；健康上报 | E `processor/stages.go` | `TestPauseSurvivesPollsAndDoesNotClaim`、`TestClaimLevel401PausesWithoutConsumingJobs` | engineering_done（unit） |
| F10 | Worker result/job/run/decision/operation 同事务；同 key 异 payload 冲突；客户端查询+同 key 有界重试，不重复推断 | S `classification.ts`；E `cairn/stages.go` | Worker 幂等/冲突测试；`internal/cairn/recovery_test.go`（真丢响应只提交 1 次；未提交同 key 重试；conflict 不重试） | engineering_done |
| F11 | 单值 accept 替换、多值累加、逐 tag reset 恢复该 tag、整维 reset 恢复自动、set_empty 区分 | E `classify/resolve.go`；S `domain.ts` | 共享 vectors（两仓字节一致，sha256 `e5d86393…d661`）在 Go/TS 双双通过；浏览器断言 | engineering_done |
| F12 | 每动作独立 UUID、仅重试复用；保存中排队不丢弃；冲突保草稿并显式重新应用；单值替换 | E `dashboard/curation-v2.js`、`common.js`、`reader.html` | 浏览器 29/29（快速双动作、reload 新 key、冲突保草稿+重应用、单值替换） | engineering_done（browser） |
| F13 | `refresh-source` 调度真实有界获取任务（保旧内容/人工数据），与 retry/replay 行为分离 | S `classification.ts`、`index.ts`；E `cairn/stages.go`、`replay.go` | Worker 路由 + Go CLI；真实 Worker 端点；未跑长周期抓取任务 | engineering_done（contract） |
| F14 | usage 原值落库、缺失标 missing；语义 spec hash 覆盖真实问题、显示 label 排除；证据预算/截断进入实际请求；alias 漂移门槛 | E `classify/{jev,questions}.go`；S `domain.ts` | `TestMissingUsageIsMarkedNotZero`、`TestSpecHashIgnoresInactiveTermsAndDisplayLabels`、`TestEvidenceBudgetBoundsTheActualRequestBody`、`TestAliasDriftBlocksCalibratedPolicy`；共享 spec hash 在真实 Worker 注册/校验通过 | engineering_done |

## 3. 真实组合证据（非 mockWorker）

### 3.1 真实浏览器 ↔ 真实 Go 服务 ↔ 真实 Worker

`tests/local-integration/browser-e2e.sh`（E 仓库，第二轮新增）：

- 真实 `wrangler dev`（真实 migrations、本地 D1/R2）+ 真实 `cairn-x-enricher serve`
  （真实 scheduler/processor/HTTP 服务）+ 真实 Chrome；
- 仅两个付费模型端点由 `tests/local-integration/mock-model.mjs` 替换（校验官方
  responses/systemone 请求形状）；
- 流程：真实服务注册 spec → 激活 v2 target → 新收藏经真实 scheduler 抓取/阅读/分类 →
  浏览器加载真实 Go 代理 → 断言与 Worker 一致的有效主题与第四主题折叠 → 人工 reject 落库、
  刷新后保持；付费模型调用 0。

结果：**16/16 PASS**。

### 3.2 真实 Go 客户端 ↔ 真实 Worker/D1

`tests/local-integration/run.sh`（E 仓库）：

- 启动真实 `wrangler dev`（真实 migrations 0001–0014，本地 D1/R2），随机端口；
- 真实 `cairn.Client` + 真实 `classify.Client` + 真实 `processor.EvidenceSnapshot`；
- 仅付费 TypeSafe 端点被独立官方合同 mock 替换（校验 questions map/type，拒绝旧 shape）；
- 流程：建收藏→claim 获取→SaveSource→SubmitEvidence→PutQuestionSpec→激活 v2 target→handshake→
  claim→Evaluate→complete→GetRuns→GetQuestionSpec→DecodeStoredJudgments→Replay（0 调用）→
  ApplyV2Override→SubmitDecision→GetV2Selection/effective；
- 断言：v2 绑定列正确、原文保留、usage 原值、replay 保留人工 reject、effective 由 decision 派生。

结果：**2/2 PASS**（全生命周期；20 轮 legacy/v2 交替 + in-flight 目标切换）。
付费模型调用 **0**；本地合同 mock 调用 **1**；replay **0**。

### 3.3 Android（设备已验证）

`./gradlew --no-daemon testDebugUnitTest lintDebug assembleDebug compileDebugAndroidTestKotlin connectedDebugAndroidTest`：

- unit **55/55**（离线队列、动作语义、冲突保留草稿）；
- **`connectedDebugAndroidTest` 11/11 PASS**，运行在真实模拟器 `plico-api26-x64`
  （Android 8.0 / API 26），覆盖多维区展示、字段动作 payload、CAS 冲突保留草稿与既有回归；
- 设备测试发现并修复真实缺陷：多维区此前从 v1 词表渲染，现从 `/api/v2-taxonomy` 加载
  （S `e08d41f`）。


## 3.4 R2-01–R2-14 修复结果（2026-09-21）

| R2 | 处理 | 证据 |
|---|---|---|
| R2-01 | 同一事务谓词守卫 links/run/decision/operation；失败无成功历史，同 key 不能变成功 | Worker `R2-01` 回归（409×2、三表 0 行、projection 不变） |
| R2-02 | claim 绑定 content revision/snapshot id/evidence hash；complete 事务内校验当前 target 与回显身份；run 用领取时 revision | `R2-02` 回归；真实集成生命周期 |
| R2-03 | 旧 curation 导入 legacy_unknown override；automatic 用 AI 结果而非人工投影；旧端点写同一 override/事件；`classification:null` 恢复真实自动值 | 3 个既有 curation/App 回归 + 新测试；真实集成 |
| R2-04 | Android flush 仅确认 Applied 才出队；离线/超时保留动作与草稿 | `V2CurationRepositoryTest`（离线/超时/混合） |
| R2-05 | 串行动作队列不丢快速编辑；冲突重放原始逻辑动作与原 key | 单测 + 设备测试断言 `reject llm` 与原 key |
| R2-06 | refresh epoch 持久意图；processor 绕过两种缓存真实抓取；成功/失败都 ack | `TestRefreshIntentBypassesSourceCaches` |
| R2-07 | 消费领取时结构化快照（全 block/role/truncation）；内容 hash 排除 fetched_at；已决请求不重复 fetch | `TestBoundEvidenceUsesTheStructuredSnapshot`、`TestEvidenceEscalationSkipsDecidedRequests` |
| R2-08 | entity_states 成功值成为实体自动基线；stale 用实体自身 revision | `R2-08` 回归 |
| R2-09 | 仅模型成功清熔断；半开探测原子限量；退避跨故障增长 | `TestCircuitBreakerBackoffGrowsAcrossFailedProbes`、`TestCircuitBreakerProbeIsAtomic` |
| R2-10 | Go DTO 接受 display_overridden；rename 不改语义身份、不阻断启动 | 真实集成 `TestLocalWorkerDisplayRenameKeepsSemantics` |
| R2-11 | 测试按候选身份赋分；每问在 instructions 绑定自己的候选，越界回退原序 | `reader_v2_test.go`（捕获请求+重复 5 次）、`service_test.go` |
| R2-12 | 各写入口比较归属收藏与规范 payload hash；异 link/payload/动作冲突 409 | `R2-12` 回归、run 同 key 异 payload |
| R2-13 | 实际 state 重算 hash、batch 语义比较、跨模型不合并、usage 汇总、超限自动分批；生产复用 opt-in 且失败回退 | reuse/batch 单测、`TestPartialReuseIsOptInAndFallsBackSafely` |
| R2-14 | includes/excludes 进入问题与 hash；spec id 内容寻址，语义变化注册新 spec，旧 spec 可重放 | excludes/label 单测、真实 rename 集成 |

R2 轮门禁：E `make verify` exit 0、`make test-ablation` exit 0、浏览器 38/38、
真实本地集成 3/3、真实浏览器 E2E 16/16；S `npm test` 117/117、typecheck、deploy:dry-run；
Android unit 60/60、lint/build/androidTest 编译与 API 26 模拟器 `connectedDebugAndroidTest` 11/11。
付费模型调用 0。

## 4. 门禁与调用计数

| 命令 | 结果 | 备注 |
| --- | --- | --- |
| E `make verify` | exit 0 | lint-ci + `go test -race` + 73/73 frontend checks + build |
| E `make test-ablation` | exit 0 | 零付费 |
| E `make test-browser` | 38/38，exit 0 | 真实 Chrome，系统 `google-chrome` |
| E `tests/local-integration/browser-e2e.sh` | 16/16 PASS | 真实 Go + Worker + Chrome |
| E `tests/local-integration/run.sh` | 2/2 PASS | 真实 Worker/D1/R2 + Go |
| S `npm test` | 113/113，exit 0 | Worker |
| S `npm run typecheck` | exit 0 | |
| S `npm run deploy:dry-run` | exit 0 | 未部署 |
| Android `testDebugUnitTest` / `lintDebug` / `assembleDebug` / `compileDebugAndroidTestKotlin` | 55/55 / BUILD SUCCESSFUL | |
| Android `connectedDebugAndroidTest`（真实模拟器 API 26） | 11/11 PASS | 设备交互已执行 |

失败→通过记录（真实执行）：F02 回归在修复前 `npx vitest run test/classification.test.ts` 为
`1 failed | 13 passed`（绑定列为 `"undefined"`），修复后 `14 passed`。

## 5. 原 126 任务与 SC01–SC30 的真实状态

**126 任务**：第二轮补齐了此前记录的工程缺口——B05-T01 映射（机器可读 + 测试 + 端点）、
B05-T10 实体生命周期与人工纠正、B05-T11/T13 提案应用与导出、B06-T05/T06/T07/T09 依据/
状态/三动作/导出、B07 Compose 页面/仓库/缓存/离线队列接入（设备验证仍 blocked）、
B09-T04/T05/T06/T09/T10/T11/T12 生命周期/补证据/重排/提案。逐任务勾选仍由 Issue 维护；
无 gold 的真实质量与无设备的交互验证仍为 blocked_external。

**SC01–SC30**（本轮重新核定）：

| 场景 | 状态 | 依据 |
| --- | --- | --- |
| SC01 | pass | 真实 Worker 上 20 轮 legacy/v2 交替全部 204，完成任务不再领取（`TestLocalWorkerVersionCompetition`） |
| SC02 | pass | 真实 in-flight 切换 + R2-01/02 事务守卫（content/lease/target 在提交边界校验） |
| SC03 | pass | 浏览器 why-only 无 classification/override |
| SC04 | pass | readingSchema + source_test |
| SC05 | pass | note-only/URL 回归 |
| SC06 | pass | policy 表驱动 |
| SC07 | pass | API + UI 分开展示 pending/processing/failed/exhausted/waiting_source 与合法空 |
| SC08 | pass | replay 0 调用；refresh/reuse 语义分离（refresh 抓取、replay 零调用、retry 不抓源） |
| SC09 | pass | 分布保留测试 |
| SC10 | pass | 实际 body 无个人字段 |
| SC11 | pass | F10 幂等 + 真丢响应恢复；R2-01 保证失败请求不落成功 operation/run |
| SC12 | pass | typed 错误映射 |
| SC13 | pass | 浏览器第四主题 + Worker |
| SC14 | pass | F11 共享 vectors + 浏览器；R2-03 旧人工导入与 reset 恢复真实自动值 |
| SC15 | pass | Web CAS 真实通过；Android 动作语义 unit + 真实设备（模拟器）交互 11/11 通过 |
| SC16 | pass | v1 写入保护 + strict 解码；R2-03 旧端点转统一 override 日志、R2-10 显示字段契约 |
| SC17 | pass | 依据面板按真实 block/角色渲染（原帖/续帖/引用/外链/第三方/unknown） |
| SC18 | pass | 预算/截断进入实际请求（F14） |
| SC19 | pass | 实体基线进入有效视图（R2-08），stale 用实体自身 revision，失败不清成功值 |
| SC20 | pass | 补证据进入下一次 provider state（R2-07），已决请求不重复 fetch，blocked/failed 保旧内容 |
| SC21 | pass（unit） | 重排回原序；真实检索未跑 |
| SC22 | pass | 分组隔离 |
| SC23 | pass | 真实空库应用 0001–0014 |
| SC24 | pass | 级联删除 |
| SC25 | pass | 全部目标 0 付费；公开产物无密钥 |
| SC26 | pass | 复用从实际 state 重算 hash、比较 batch 语义、跨模型不合并、usage 汇总；生产 opt-in 并回退 |
| SC27 | pass | `make test-ablation` |
| SC28 | pass（browser）/ 设备未运行 | 29/29 |
| SC29 | pass | 漂移门槛进入 Decide |
| SC30 | pass | 扩展触发/预算/回退 + R2-07 去重与 R2-09 熔断预算（不耗尽业务任务） |

## 6. 代码 PR 与设计偏离

- E PR #17（Draft，base main，head `impl/jev-v2-fixes-20260921`）——F01/F03–F14 与 B06/B09 的 Enricher 侧。
- S PR #32（Draft，base main，head `impl/jev-v2-fixes-20260921`）——F02/F04–F13 与 B05/B07/B09 的 Worker/Android 侧。
- 设计偏离（有据）：
  1. provider `instructions`/`criteria` 在内部以 `json.RawMessage` 保存并在 hash 时规范化，以同时满足
     结构化支持与跨语言稳定 hash；
  2. spec 身份在启用 Score 时改为 `classify-v1-score`（Worker 同一 id 不接受不同 bytes）；
  3. 有效投影由 Worker 服务端从 decision+override 派生，replay commit 只追加 decision，不新建 run；
  4. 组件暂停采用进程内熔断 + 有界探测；进程重启会重置熔断（Worker target 仍是权威），已记录为限制。

## 7. 未验证项、风险与回滚

未验证/阻塞：

- 真实模型质量（无人工 gold、无 live 授权）→ quality_verified=false，inconclusive；
- R2 复审发现已在 E `1462fb4`/S `4632e86` 修复并有回归与真实集成证据；等待独立复审；
- Android 设备执行（无设备）→ device 未运行（unit/lint/build/androidTest 编译均通过）；
- 长周期 refresh-source 抓取、真实扩展质量收益 → retest_required；
- 独立复审 → review_accepted=false。

风险：

1. 付费提供方响应可能增加未声明字段（strict 解码会失败）→ 按 typed contract 错误处理，不静默吞；
2. 旧 Worker（无 v2 端点）会降级 v1 且不写 v2 历史 → 已在启动日志与 evidence 明示；
3. 熔断是进程内状态，重启后立即探测 → 单次探测成本有界。

回滚：

- 关闭扩展 flag 只停止该能力，不清历史快照/成功实体/人工提案；
- 目标回滚使用新 generation 指向旧 spec，不倒退 generation；
- 应用回滚保留新增历史与人工数据，不使用破坏性 down 迁移；
- v2 写入回退只停写，不删已有 run/decision/override。

## 8. 分类结论（分列）

| 维度 | 结论 |
| --- | --- |
| 工程完成 | F01–F14 与 B05/B06/B07/B09 已记录工程缺口均补齐；SC26/B04-T06/B08 生产导出完成；Android 设备测试通过 |
| 真实质量 | 未验证（缺 gold/live），不得以工程测试替代 |
| 设备验证 | 已在真实模拟器（API 26）执行 connectedDebugAndroidTest 11/11；真机未运行 |
| 独立审查 | 未通过；等待独立复审，本报告不构成自批 |

## 9. 未执行的生产操作

无 merge、无 Ready 转换、无 tag、无远端 D1 迁移、无 Worker/NAS/App 发布、无收费回填、
无 force-push、无历史分支删除。

## 10. 请求的下一步

请所有者/独立审查者：

1. 在 E `faad44c` / S `4c03094`（及本报告文档提交）上独立复核；
2. 对 F01–F14 逐项判定是否接受，对原 126 任务决定例外或后继 Issue；
3. 只有独立复审通过后才讨论合并/部署授权。

**执行者不自行批准、不合并、不部署。**


</details>
