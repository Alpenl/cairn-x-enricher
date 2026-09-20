# FINAL-REVIEW：Cairn Jev v2 跨仓库最终复审证据

日期：2026-09-20
总控：[E #10](https://github.com/Alpenl/cairn-x-enricher/issues/10)
实现分支：E `impl/jev-v2-b02-runtime-safety`，S `impl/jev-v2-b01-authoritative-target`

> 本文是**证据快照**，不是发布授权，也不代表全部 126 任务已验收。Issue 是实时状态的唯一来源。

## 1. 基线与最终 SHA

| 仓库 | 审查基线 | 实现 HEAD | 分支 |
| --- | --- | --- | --- |
| cairn-x-enricher (E) | `cb13d0e543e54f18baf6e151630d728b3175e792` | `e6956c6163457c413a7f1a0b78719807c0ee71ca` | `impl/jev-v2-b02-runtime-safety` |
| cairn-share (S) | `5cf3b0d45c8fbae07d0fb5be1772e7d747c69b4b` | `667625cdcb915d158f070efe1b9a9a798f865442` | `impl/jev-v2-b01-authoritative-target` |

Schema/spec/model/policy 版本：

- Worker 迁移：`0010_authoritative_classification_target.sql`、`0011_replayable_domain.sql`、`0012_multidimensional_taxonomy.sql`（0009 未改动）。
- 分类 spec：`classify-v1`（`internal/cairn.SpecID` 与 `CompileSpec` 一致）。
- Policy：`jev-policy-v2`（`classify.PolicyVersion`，未校准）。
- Taxonomy：`2026-09-20.1`，`definition_version: 1`。
- Feature flags：`entities`/`evidence`/`rerank`/`proposal` 全部 **默认 off**。

## 2. 批次与证据

| 批次 | Issue | 状态 | 证据 | 主要实现 |
| --- | --- | --- | --- | --- |
| B01 | S #28 | engineering_done | S `docs/jev-v2/evidence/B01.md` | 权威目标/握手/typed 错误/幂等/note-URL 解耦 |
| B02 | E #11 | engineering_done | E `.../B02.md` | 显式确认、ReadingResult、握手、暂停、有界 shutdown |
| B03 | S #29 | engineering_done | S `.../B03.md` | 可重放域模型、snapshot/spec/run/override/event |
| B04 | E #12 | engineering_done | E `.../B04.md` | typed 判断、纯 Decide/Resolve/Replay、CLI |
| B05 | S #30 | engineering_done（T01/T10/T13 部分） | S `.../B05.md` | 多维词表、v1 写入保护、提案 |
| B06 | E #13 | 部分 | E `.../B06.md` | 多维整理 UI + 真实浏览器验收 |
| B07 | S #31 | 部分（设备未运行） | S `.../B07.md` | Kotlin v2 DTO、字段动作、离线队列 |
| B08 | E #14 | engineering_done，quality_verified=false | E `.../B08.md` | 离线 scorer/校准/消融/晋升门禁 |
| B09 | E #15 | 部分（扩展默认 off） | E `.../B09.md` | 实体/受控抓取/重排/提案 |
| B10 | E #16 | 本文件 | E `.../B10.md` | 集成/故障/迁移/隐私/文档/复审 |

## 3. R01–R39 覆盖（按主责批次）

| 审查项 | 主责 | 证据位置 |
| --- | --- | --- |
| R01 边界与已有基础 | B04 | `internal/classify/*`、B04.md |
| R02 legacy 输出兼容 | B04 | `jev.go` v1 投影、B04.md |
| R03–R06 维度/概念/展示上限 | B05 | `taxonomy-v2.ts`、B05.md |
| R07 概率与重要性 | B04 | `policy.go` Score 分离、B04.md |
| R08–R11 弃权/空值/阈值/confidence | B04 | `policy.go`、`resolve.go`、B04.md |
| R12/R14 原语/结构化问题/fan-out | B04/B09 | `primitives.go`、`questions.go`、B09.md |
| R13 候选与展示 | B05 | `taxonomy-routes.ts`、B05.md |
| R15–R17 来源/隔离/预算语言 | B03/B04/B09 | `evidence.go`、B09.md |
| R18–R21 分层/失效/历史/漂移 | B03/B04 | `domain-routes.ts`、B08.md |
| R22 版本竞争 P0 | B01 | `classification.ts`、SC01 测试 |
| R23 隐式确认 P0 | B02 | `reader.js`/`curation-v2.js`、B02/B06.md |
| R24 原文重复输出 | B02 | `source.go` readingSchema、B02.md |
| R25 错误与幂等 | B01/B02 | `errors.go`、`classification.ts`、B01/B02.md |
| R26–R29 依据/状态/覆盖/反馈 | B03/B06 | `curation-v2.js`、B06.md（部分） |
| R30–R33 实体/补证据/重排/治理 | B09 | `internal/extension/*`、B09.md |
| R34–R36 gold/找回/消融 | B08 | `experiments/classification/*`、B08.md（gold 阻塞） |
| R37 兼容与遗留清理 | B05/B07/B10 | B05/B07/B10 证据 |
| R38 安全与预算 | B01/B03/B09 | `fetch.go`、`classification.ts`、B09.md |
| R39 验收与复审 | B10 | 本文件 |

## 4. SC01–SC30 结果

| 场景 | 结果 | 证据 |
| --- | --- | --- |
| SC01 A/B 交替 ≥20 轮 | ✅ 通过 | `classification.test.ts` 20 轮测试 + `integration_test.go` |
| SC02 源/目标变化旧结果不覆盖 | ✅ 通过 | `guards completion against a target switch` |
| SC03 只 why/status 无分类 | ✅ 通过 | `frontend-check.mjs` + 浏览器 why-only 断言 |
| SC04 阅读 schema 不含原文 | ✅ 通过 | `readingSchema` + `source_test.go` |
| SC05 note-only 不重抓、URL 变化保留人工 | ✅ 通过 | `index.test.ts` 两个回归 |
| SC06 两强一模糊局部弃权 | ✅ 通过 | `policy_test.go` 场景表 + Go 测试 |
| SC07 none/词表外/未运行/失败分开 | ✅ 通过 | `policy.go` verdict + `FieldStatus` |
| SC08 阈值/display 重放 0 调用 | ✅ 通过 | `Replay` + `policy_test.go` |
| SC09 同均值不同分布保留 | ✅ 通过 | `DescribeDistribution` 测试 |
| SC10 客观 body 无个人字段 | ✅ 通过 | `TestObjectiveStateExcludesPersonalFields` |
| SC11 完成响应丢失不双 run | ✅ 通过 | operation key 幂等测试（Go + Worker） |
| SC12 401/422 vs 429/409 | ✅ 通过 | `errors.go`、`APIError.Class()`、集成测试 |
| SC13 四主题底层完整、v1 合法 | ✅ 通过 | 浏览器 `fourth topic folded` + Worker 选择测试 |
| SC14 reject/set-empty/reset 区分 | ✅ 通过 | `resolve.go`、Worker override 测试、浏览器 |
| SC15 并发 CAS 不丢更新 | ✅ 通过（逻辑层） | `expected_revision` + `revision_conflict`；设备未运行 |
| SC16 v1 读写不破坏 v2 | ✅ 通过 | `applyV1Write` + Worker v1 写入测试 |
| SC17 引用/续帖/第三方分离 | ⚠️ 部分 | 角色枚举已定义；UI block 级渲染为 partial |
| SC18 超长/截断明确状态 | ✅ 通过 | `PrepareEvidence` truncation 测试 |
| SC19 实体 not_run/failed 不清成功值 | ⚠️ 部分 | 存储与状态枚举具备；写入联动 partial |
| SC20 补证据防 SSRF | ✅ 通过（适配器） | `fetch.go` + SSRF 测试；触发流程 partial |
| SC21 重排失败原序、权限不变 | ✅ 通过 | `Rerank` 测试 |
| SC22 train/dev/test 隔离 | ✅ 通过 | `SplitByGroup` 泄漏测试 |
| SC23 空库/迁移通过 | ✅ 通过 | 0010–0012 空库与历史库应用 |
| SC24 删除覆盖全部私人数据 | ✅ 通过 | 级联删除测试（Go 域表 + v2 selection） |
| SC25 普通 CI 不收费、无密钥 | ✅ 通过 | 所有目标零付费；live 路径显式拒绝 |
| SC26 部分重评合法复用 | ✅ 通过 | `CacheKey` 测试 |
| SC27 legacy 实验可回归 | ✅ 通过 | `make test-ablation` |
| SC28 真实浏览器键盘/手机/dirty | ✅ 通过（浏览器）/⚠️ 设备未运行 | `tests/browser/run.mjs` 21 项 |
| SC29 alias 漂移不沿用校准 | ✅ 通过 | `DetectModelDrift` 测试 |
| SC30 预算/取消/耗尽无隐式全库 | ✅ 通过 | `Ledger` 测试 + 扩展默认 off |

## 5. 新增/变更合同与迁移

- **目标合同**：`server desired target = spec + requested model + policy + monotonic generation`；claim 绑定，complete/fail 校验目标、输入 revision、lease。
- **幂等**：`operation_key` + payload hash；相同返回既有结果，不同冲突。
- **域模型**：`evidence_snapshots`/`question_specs`/`classification_runs`/`curation_overrides`/`curation_events`/`current_projections`/`entity_states`/`budget_ledger`。
- **多维选择**：`link_selections_v2`；v1 投影仅取前 3 topic + form/use，隐藏维度保留。
- **迁移**：仅新增，0009 字节未改动；非破坏性 up，无破坏性 down。

## 6. P0 失败→通过证据

- **隐式确认**：修改前 why-only 保存携带 `classification`；现断言不含（`frontend-check.mjs`、浏览器）。
- **版本竞争循环**：修改前 A/B 交替重复领取；现 20 轮全 204、`attempts=1`。
- **配置错误耗尽队列**：修改前配置错误计为 job 失败；现暂停组件、不计失败（`TestClassificationConfigurationErrorPausesInsteadOfBurningQueue`）。
- **note 编辑销毁原文**：修改前 note 变化清空 `original_text`；现保留快照与译文。

## 7. 模型调用计数

- 重放/阈值/display：**0**。
- 失败恢复（响应丢失重试）：**0 额外模型调用**（operation key 幂等）。
- 普通 `make verify`/CI/浏览器测试：**0**。
- live：**未运行**（无授权）。

## 8. 工程门禁 vs 真实质量 vs 缺失前置

| 类别 | 结果 |
| --- | --- |
| E `make verify` | 退出码 0 |
| E `make test-ablation` | 退出码 0 |
| E `make test-browser` | 21/21 通过 |
| S `npm test` | 101/101 通过 |
| S `npm run typecheck` / `deploy:dry-run` | 退出码 0 |
| Android `testDebugUnitTest` / `lintDebug assembleDebug compileDebugAndroidTestKotlin` | BUILD SUCCESSFUL |
| 真实模型质量 | **未验证**（无人工 gold、无 live 授权） |
| 真实设备 UI | **未运行**（无授权设备） |

## 9. Flags 与回滚

| Flag | 实现 | 测试 | 真实收益 | 默认 |
| --- | --- | --- | --- | --- |
| entities | 是 | 是 | 未验证 | off |
| evidence | 适配器是，触发 partial | 是 | 未验证 | off |
| rerank | 是 | 是 | 未验证 | off |
| proposal | 是 | 是 | 未验证 | off |

回滚：关闭 flag 只停止该扩展，不清历史快照/成功实体/人工提案；重排关闭立即回原序；目标回滚使用新 generation 指向旧 spec（不倒退）；应用回滚保留新增历史。

## 10. 未解决风险与偏离

1. **无真实 gold**：B08 质量结论 inconclusive，未晋升；不得以工程测试冒充质量。
2. **B06/B07 部分 UI**：block 级依据渲染、独立分类状态机、三动作按钮、Compose 多维页面、Markdown v2 导出为 partial。
3. **B05-T01/T10/T13 部分**：完整 v1→v2 映射梳理、实体算法联动、导出 partial。
4. **B09-T04/T05/T07/T08 部分**：实体生命周期写入、补证据触发、OCR 适配器 partial。
5. **设备/浏览器**：真实浏览器已运行；真实 Android 设备未运行。
6. **生产**：未执行远端迁移、未部署、未收费回填。

## 11. 未执行的生产操作

- 无远端 D1 迁移、无 Worker 部署、无 NAS 镜像、无 App 发布、无收费回填、无 merge、无 tag、无 force-push。

## 12. 请求的下一步

请所有者/独立审查者：

1. 在锁定的两仓 SHA 上独立复核 diff 与测试；
2. 决定是否接受部分实现为例外或建立后继 Issue；
3. 如需继续，批准独立审查后再决定合并/部署授权。

**执行者不自行批准、不合并、不部署。**
