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
| cairn-x-enricher (E) | `4031200984d819b59baea593025621a04deeb984` | `faad44c` | `impl/jev-v2-fixes-20260921` / E PR #17（Draft） |
| cairn-share (S) | `8f98ac0a9d6e5257840bd518cc5960464b74a98c` | `4c03094` | `impl/jev-v2-fixes-20260921` / S PR #32（Draft） |

本报告本身是后续纯文档提交：报告提交 SHA 见 E 分支上 `docs(jev-v2): final review for the
F01–F14 fix round` 的 commit（`git log -1 --format=%H -- docs/jev-v2/FINAL-REVIEW.md`）。
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

`tests/local-integration/run.sh`（E 仓库）：

- 启动真实 `wrangler dev`（真实 migrations 0001–0014，本地 D1/R2），随机端口；
- 真实 `cairn.Client` + 真实 `classify.Client` + 真实 `processor.EvidenceSnapshot`；
- 仅付费 TypeSafe 端点被独立官方合同 mock 替换（校验 questions map/type，拒绝旧 shape）；
- 流程：建收藏→claim 获取→SaveSource→SubmitEvidence→PutQuestionSpec→激活 v2 target→handshake→
  claim→Evaluate→complete→GetRuns→GetQuestionSpec→DecodeStoredJudgments→Replay（0 调用）→
  ApplyV2Override→SubmitDecision→GetV2Selection/effective；
- 断言：v2 绑定列正确、原文保留、usage 原值、replay 保留人工 reject、effective 由 decision 派生。

结果：**PASS**。付费模型调用 **0**；本地合同 mock 调用 **1**；replay **0**。

## 4. 门禁与调用计数

| 命令 | 结果 | 备注 |
| --- | --- | --- |
| E `make verify` | exit 0 | lint-ci + `go test -race` + 73/73 frontend checks + build |
| E `make test-ablation` | exit 0 | 零付费 |
| E `make test-browser` | 29/29，exit 0 | 真实 Chrome，系统 `google-chrome` |
| E `tests/local-integration/run.sh` | PASS | 真实 Worker/D1/R2 + Go |
| S `npm test` | 107/107，exit 0 | Worker |
| S `npm run typecheck` | exit 0 | |
| S `npm run deploy:dry-run` | exit 0 | 未部署 |
| Android `testDebugUnitTest`/`lintDebug`/`assembleDebug`/`compileDebugAndroidTestKotlin` | **未运行（本轮）** | 编译/设备不等同运行；上轮 BUILD SUCCESSFUL 记录保留 |

失败→通过记录（真实执行）：F02 回归在修复前 `npx vitest run test/classification.test.ts` 为
`1 failed | 13 passed`（绑定列为 `"undefined"`），修复后 `14 passed`。

## 5. 原 126 任务与 SC01–SC30 的真实状态

**126 任务**：本轮只完成 F01–F14 及其联动；不替任何原 B 任务勾选完成。逐任务状态仍以
Issues 为准（E #11–#15、S #28–#31）。工程缺口仍包括：B05-T01 映射梳理、B06 block 级依据/
独立状态/三动作 UI 细节、B07 Compose 页面与离线队列接入/设备验证、B09-T04/T05/T07/T08
生命周期与触发。缺 gold/设备只阻塞相应验证，不解释这些工程项。

**SC01–SC30**（本轮重新核定）：

| 场景 | 状态 | 依据 |
| --- | --- | --- |
| SC01 | partial / retest_required | Worker 20 轮 + 真实 v2 claim/complete；真实 20 轮 Go 交替未跑 |
| SC02 | partial | target 切换与 run_stale 已测；in-flight 切换的真实并发未跑 |
| SC03 | pass | 浏览器 why-only 无 classification/override |
| SC04 | pass | readingSchema + source_test |
| SC05 | pass | note-only/URL 回归 |
| SC06 | pass | policy 表驱动 |
| SC07 | partial | API 状态分离；UI 状态细节仍 partial |
| SC08 | pass | replay 0 调用 + F06 真实往返 |
| SC09 | pass | 分布保留测试 |
| SC10 | pass | 实际 body 无个人字段 |
| SC11 | pass | F10 幂等 + 真丢响应恢复 |
| SC12 | pass | typed 错误映射 |
| SC13 | pass | 浏览器第四主题 + Worker |
| SC14 | pass | F11 共享 vectors + 浏览器 |
| SC15 | partial | Web CAS 已验证；Android 设备未运行 |
| SC16 | pass | v1 写入保护 + strict 解码 |
| SC17 | partial | 角色枚举；UI block 级渲染 partial |
| SC18 | pass | 预算/截断进入实际请求（F14） |
| SC19 | partial | 存储/状态具备；扩展触发流程未接 |
| SC20 | partial | 适配器与 SSRF 测试；触发流程未接 |
| SC21 | pass（unit） | 重排回原序；真实检索未跑 |
| SC22 | pass | 分组隔离 |
| SC23 | pass | 真实空库应用 0001–0014 |
| SC24 | pass | 级联删除 |
| SC25 | pass | 全部目标 0 付费；公开产物无密钥 |
| SC26 | partial | cache/spec 语义测试；部分重评端到端未跑 |
| SC27 | pass | `make test-ablation` |
| SC28 | pass（browser）/ 设备未运行 | 29/29 |
| SC29 | pass | 漂移门槛进入 Decide |
| SC30 | partial | 预算/flag 具备；扩展触发端到端未跑 |

## 6. 代码 PR 与设计偏离

- E PR #17（Draft，base main，head `impl/jev-v2-fixes-20260921`）——F01/F03–F14 的 Enricher 侧。
- S PR #32（Draft，base main，head `impl/jev-v2-fixes-20260921`）——F02/F04–F13 的 Worker 侧。
- 设计偏离（有据）：
  1. provider `instructions`/`criteria` 在内部以 `json.RawMessage` 保存并在 hash 时规范化，以同时满足
     结构化支持与跨语言稳定 hash；
  2. spec 身份在启用 Score 时改为 `classify-v1-score`（Worker 同一 id 不接受不同 bytes）；
  3. 有效投影由 Worker 服务端从 decision+override 派生，replay commit 只追加 decision，不新建 run；
  4. 组件暂停采用进程内熔断 + 有界探测；进程重启会重置熔断（Worker target 仍是权威），已记录为限制。

## 7. 未验证项、风险与回滚

未验证/阻塞：

- 真实模型质量（无人工 gold、无 live 授权）→ quality_verified=false，inconclusive；
- Android 设备执行（无设备）→ device 未运行；
- 长周期 refresh-source 抓取、20 轮真实 A/B、in-flight target 切换、扩展触发端到端 → retest_required；
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
| 工程完成 | F01–F14 已实现并有最低回归；原 126 任务中 B05/B06/B07/B09 仍有未完成工程 |
| 真实质量 | 未验证（缺 gold/live），不得以工程测试替代 |
| 设备验证 | 未运行（缺设备） |
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
