# Jev v2：设计、任务与代码审查

> 后续所有者授权和 B08 自动评估口径见 [2026-09-22 更新](AUTHORIZATION-20260922.md)。下文历史授权/人工标注前置条件与该更新冲突时，以更新为准。

工作流版本：`issues-v1`，2026-09-20。本目录是版本化设计与验收规范，**不是实施进度表，也不表示功能已实现**。

唯一总控：[Issue #10](https://github.com/Alpenl/cairn-x-enricher/issues/10)。[ISSUE-INDEX.md](ISSUE-INDEX.md)列出全部真实任务；[EXECUTION.md](EXECUTION.md)是DeepSeek执行协议；[REVIEW.md](REVIEW.md)保留完整R01–R39审查；[MIGRATION.md](MIGRATION.md)记录旧计划PR到Issue的迁移。

## 职责分离

| 载体 | 负责内容 | 不负责内容 |
|---|---|---|
| 设计文档PR #3 / 本目录 | 架构原则、合同、验收场景、历史映射、静态规格 | 不维护第二套任务进度、不声称代码完成 |
| 总控Issue #10 | 范围、批次关系、跨仓库协调、最终验收 | 不是代码合并入口 |
| B01–B10实施Issues | 126项任务的实时状态、依赖/阻塞、实现PR和证据 | 不强制一批对应一个PR |
| 后续真实代码PR | 一个可审查改动、测试/迁移/必要文档、任务ID映射 | 不替整个跨端Issue提前宣布完成 |

原E #4–#9与S #24–#27是被替代的计划PR，不再用于编码。保留其分支/提交作历史引用；不要向旧计划分支补实现，也不要再次建立只含待办的占位PR。E=Alpenl/cairn-x-enricher，S=Alpenl/cairn-share。

## 设计基线与目标

审查基线E `cb13d0e543e54f18baf6e151630d728b3175e792`，S `5cf3b0d45c8fbae07d0fb5be1772e7d747c69b4b`。每次执行重新检查实际main、工作区与两仓SHA，不把审查基线当新实现版本。

目标：`EvidenceSnapshot → QuestionSpec → RawJudgments → DecisionPolicy → AIProposals → HumanOverrides → EffectiveView`。Evaluate可联网，Decide/Resolve是纯计算。保留单Go进程、Worker持久化队列、source-first、lease/revision、人工优先、旧App兼容、受控图片。不把Redis、向量库、RAG、微服务、图编排或全库回填设为前提。

## 静态工作分解

完整可勾选任务及当前状态只在对应Issue；以下文件只保存设计边界，不复制动态进度。

| 批次 | 任务数 | 规格 | 任务 |
|---|---:|---|---|
| B01 | 10 | [Worker安全](01-worker-safety.md) | [S #28](https://github.com/Alpenl/cairn-share/issues/28) |
| B02 | 11 | [运行时/显式确认](02-runtime-safety.md) | [E #11](https://github.com/Alpenl/cairn-x-enricher/issues/11) |
| B03 | 14 | [领域持久化](03-domain-storage.md) | [S #29](https://github.com/Alpenl/cairn-share/issues/29) |
| B04 | 15 | [判断与策略](04-judgment-policy.md) | [E #12](https://github.com/Alpenl/cairn-x-enricher/issues/12) |
| B05 | 14 | [词表与协议](05-taxonomy-api.md) | [S #30](https://github.com/Alpenl/cairn-share/issues/30) |
| B06 | 12 | [Web整理](06-curation-ui.md) | [E #13](https://github.com/Alpenl/cairn-x-enricher/issues/13) |
| B07 | 10 | [Android兼容](07-android-compat.md) | [S #31](https://github.com/Alpenl/cairn-share/issues/31) |
| B08 | 14 | [评估校准](08-evaluation.md) | [E #14](https://github.com/Alpenl/cairn-x-enricher/issues/14) |
| B09 | 14 | [有界扩展](09-semantic-extensions.md) | [E #15](https://github.com/Alpenl/cairn-x-enricher/issues/15) |
| B10 | 12 | [集成复审](10-release-proof.md) | [E #16](https://github.com/Alpenl/cairn-x-enricher/issues/16) |

## 依赖不是预设分支链

B08基线/授权准备先于B01；B02的UI/ReadingResult可与B01并行，握手接入依赖B01。B03基于B01冻结合同，与B02核对；B04接B02/B03；B05接B03/B04。B06/B07基于B05合同可并行并共享交互fixture。B08工具接B03/B04，不能等待Web/Android完成才准备评估；B09接判断/后端/离线评估工具；B10最终验收全部交付。精确依赖及各阶段条件见Issue正文和ISSUE-MAP.json。

默认从最新main创建真实实现分支。仅确实需要未合并代码时使用短堆叠并记录上游PR/SHA；未经授权不得为了继续开发自行合并main。可在隔离工作树联调明确的实现SHA，但不能把依赖标为已合并。批次可以拆多个PR，拆分保留原任务ID和所有验收。

## 分析到任务的覆盖

| 审查项 | 主责 | 必须联动 |
|---|---|---|
| R01–R02 边界与已有基础 | B04 | B01/B03/B10 |
| R03–R06 维度、概念、展示上限 | B05 | B04/B06/B07 |
| R07 概率与重要性 | B04 | B06/B09 |
| R08–R11 弃权、空值、阈值、confidence | B04 | B03/B05/B06/B08 |
| R12–R14 原语、结构化问题、fan-out | B04 | B08/B09 |
| R15–R17 来源、个人隔离、预算语言 | B03/B04 | B08/B09 |
| R18–R21 分层、失效、历史、模型漂移 | B03/B04 | B01/B08/B10 |
| R22 版本竞争P0 | B01 | B02/B10 |
| R23 隐式确认P0 | B02 | B03/B06/B07/B08 |
| R24 原文重复输出 | B02 | B04/B10 |
| R25 错误与幂等 | B01/B02 | B03/B04/B10 |
| R26–R29 依据、状态、覆盖、反馈 | B03/B06 | B05/B07/B08 |
| R30–R33 实体、补证据、重排、词表治理 | B09 | B03/B04/B05/B06/B07/B08 |
| R34–R36 gold、找回、消融 | B08 | 基线提前、B09/B10 |
| R37 兼容与遗留清理 | B05/B07/B10 | B02/B03/B04/B06 |
| R38 安全与预算 | B01/B03/B09 | 所有批次 |
| R39 验收与复审 | B10 | 所有批次 |

## 完成与授权

Issue状态使用not_started、in_progress、blocked_external、ready_for_review、accepted；工程实现、质量验证、审查、合并、部署分别记录。勾任务要有真实commit/test证据；关闭批次需全部验收及独立审查，部分实现PR只普通引用。文档PR合并不关闭总控；真实gold/设备/授权缺失不得伪造，也不能以未部署掩盖代码未写。

收费模型、全库回填、远端迁移、Worker/NAS/App发布均需要单独授权。DeepSeek是代码执行者，不是更换生产供应商的指令。不公开私人收藏、密钥、原文与标注。
