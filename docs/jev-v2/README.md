# 分类决策与人工整理 v2：执行总入口

**计划状态：待实现。本文档和这一组草稿 PR 不表示功能已完成。**

本轮将完整分析落成 1 个总计划 PR + 10 个工作批次 PR。DeepSeek 是编码执行者，不是要求把生产 Grok/Jev 换成 DeepSeek。未经所有者另行授权，不合并、不发版、不迁移远端 D1、不改 NAS、不调用付费模型、不批量回填、不发布私人收藏或原文。

先读 [REVIEW.md](REVIEW.md) 的 R01–R39，再读 [EXECUTION.md](EXECUTION.md)。实际 PR 链接统一记录在本分支的 `PR-INDEX.md`；批次文件在各自草稿 PR 分支。下游分支后续需要同步上游新增的实现提交，创建时继承了计划不代表自动继承未来代码。

## 1. 基线与目标

| 仓库 | 已核对基线 | 职责 |
|---|---|---|
| Alpenl/cairn-x-enricher | cb13d0e543e54f18baf6e151630d728b3175e792 | Go 获取/阅读/分类、同源 Web、评估及集成 |
| Alpenl/cairn-share | 5cf3b0d45c8fbae07d0fb5be1772e7d747c69b4b | Worker/D1/R2 契约和数据、词表、Android |

目标结构：`EvidenceSnapshot -> QuestionSpec -> RawJudgments -> DecisionPolicy -> AIProposals -> HumanOverrides -> EffectiveView`。Evaluate 可以联网；Decide/Resolve 不得联网。先修正确性，再引入 v2 字段和交互，最后由真实评估决定哪些能力能默认启用。

保留：单 Go 进程、Worker 持久化队列、source-first、独立 lease/revision、人工优先、旧 App 兼容、受控图片存储。不是本轮前置条件：Redis、向量库、RAG、微服务、图编排框架、全库重跑。

## 2. 草稿 PR 与任务所有权

下面编号是批次 ID，不是 GitHub PR number。代码实现继续提交到对应草稿 PR 的 head branch，不另建重复 PR。所有任务默认未完成。

| 批次 | 仓库 | 分支 / 计划文件 | 本批负责 | 实现前依赖 |
|---|---|---|---|---|
| B00 | enricher | plan/jev-v2-00-master | 完整审查、执行协议、契约原则、验收矩阵 | 无 |
| B01 | share | plan/jev-v2-01-worker-safety / 01-worker-safety.md | Worker 目标规格权威、旧消费者隔离、备注局部失效、P0 回归 | B00 |
| B02 | enricher | plan/jev-v2-02-runtime-safety / 02-runtime-safety.md | 显式确认标签、独立 ReadingResult、错误/幂等/握手 | B01 |
| B03 | share | plan/jev-v2-03-domain-storage / 03-domain-storage.md | 可重放快照/runs/decisions、字段覆盖事件、v2 内部契约 | B01、B02 契约核对 |
| B04 | enricher | plan/jev-v2-04-judgment-policy / 04-judgment-policy.md | typed evaluator、结构化问题、纯策略、重放、客观证据隔离 | B02、B03 |
| B05 | share | plan/jev-v2-05-taxonomy-api / 05-taxonomy-api.md | 标签 v2、稳定映射、公共 opt-in 投影、分面/搜索/实体/治理 API | B03、B04 契约与样例 |
| B06 | enricher | plan/jev-v2-06-curation-ui / 06-curation-ui.md | 字段级整理、候选/依据、独立状态、筛选/导出 | B04、B05 |
| B07 | share | plan/jev-v2-07-android-compat / 07-android-compat.md | Android v2 消费/整理、缓存、旧协议全矩阵 | B05、B06 交互契约 |
| B08 | enricher | plan/jev-v2-08-evaluation / 08-evaluation.md | gold 数据格式、离线指标、消融、校准、模型晋升门禁 | B04、B06；基线冻结在 B01 前准备 |
| B09 | enricher | plan/jev-v2-09-semantic-extensions / 09-semantic-extensions.md | 实体候选验证、按需补证据、语义重排、词表提案 | B04、B05、B08 工具链 |
| B10 | enricher | plan/jev-v2-10-release-proof / 10-release-proof.md | 跨仓库集成、迁移/回滚演练、文档清理、最终证据包 | B01–B09 |

同仓库 PR 堆栈：

```text
enricher main -> B00 -> B02 -> B04 -> B06 -> B08 -> B09 -> B10
share    main -> B01 -> B03 -> B05 -> B07
```

推荐单执行者顺序：B00 基线冻结 -> B01 -> B02 -> B03 -> B04 -> B05 -> B06 -> B07 -> B08 -> B09 -> B10。B08 的数据目录/授权检查/现有 v1 记录冻结在开始时做；不必等新分类写完再找基线。不同仓库有代码依赖时使用固定 companion SHA，不测试漂移的 main。

B03 先实现通用数据与内部接口；B04 用冻结的 v2 合同 fixture 和仍可读取的旧词表开发；B05 再部署可执行新词表与公共投影，因此不存在 B04 与 B05 相互等待的实现环。

## 3. 决策冻结原则

1. Worker 的目标规格是权威。消费者只能声明支持，不得以自身版本改变目标。目标带单调 generation，完成必须匹配 job 的 generation 和输入 revision。
2. 把 evidence、question set、model、policy、display 分开版本化。display 改名和 policy 阈值调整不触发模型调用；原始命题/实际 state 变化不能伪装成无损重放。
3. 不把“模型分布”“程序决定”“人工选择”写成同一份不可区分的数据。未覆盖、明确清空、逐标签拒绝具有不同语义。
4. v2 是新契约；v1 compatibility projection 维持旧 key 和类型。旧客户端写入不得抹掉其不认识的 v2 维度、隐藏标签或拒绝记录。
5. 主题是否成立与主题重要性分开。没有深度判断时用明确稳定展示顺序，不把独立 Noul 的值当跨主题重要性分。
6. 合法空值可 completed。只有有可观察依据或专门判断支持时才给 not_applicable/out_of_taxonomy 等原因；不能从 p=0.5 猜出缺材料。
7. 客观 state 排除 note、why、curation。个人状态是独立维度。引用不等于赞同，不能从收藏行为推断立场。
8. 模型决定不得改 source、用户状态、人工原因。所有写入都受 lease/revision/spec/目标检查，重试提交幂等。
9. 新增迁移，不重写已发布的 0009。先兼容后端与客户端，再人工批准启用。关闭功能不销毁原文/人工数据。
10. 深化能力要有真实收益；实体、补材料、重排、自动校准晋升默认关闭，完成实现不等于允许上线。

字段/端点名字在各批次中是拟定契约，实施时先检索当前代码、生成 fixture，再冻结。不允许悄悄把不存在的 CLI 或测试 target 写成已可运行。

## 4. 分析 → 实施追踪（不得遗漏）

| 审查项 | 主责批次 | 必须补充的联动 |
|---|---|---|
| R01–R02 保留边界、不是再包装 API | B04 | B01/B03/B10 |
| R03–R05 维度与概念关系 | B05 | B04/B06/B07 |
| R06 展示上限与存储分离 | B05 | B04/B06/B07，双端校验 |
| R07 概率与重要性区分 | B04 | B06/B09 |
| R08–R11 局部弃权、合法空、阈值、confidence | B04 | B03/B05/B06/B08 |
| R12–R14 Noul/Choice/Score、结构定义、fan-out | B04 | B08/B09 |
| R15–R17 来源角色、个人隔离、长度与语言 | B03、B04 | B08/B09 |
| R18–R21 分层、失效、可重放历史、模型漂移 | B03、B04 | B01/B08/B10 |
| R22 版本交替重跑 P0 | B01 | B02/B10 |
| R23 隐式确认 P0 | B02 | B03/B06/B07/B08 |
| R24 原文重复输出 | B02 | B04/B10 |
| R25 错误类型与幂等 | B01、B02 | B03/B04/B10 |
| R26–R29 依据、状态、覆盖与反馈 | B03、B06 | B05/B07/B08 |
| R30 实体独立生命周期 | B09 | B03/B05/B06/B07 |
| R31 按需补证据 | B09 | B03/B04/B08/B10 |
| R32 召回与重排 | B09 | B05/B06/B08 |
| R33 词表治理 | B05、B09 | B04/B08 |
| R34–R36 gold、找回/负担、消融 | B08 | B00/B01 前基线、B09/B10 |
| R37 兼容与遗留清理 | B05、B07、B10 | B02/B03/B04/B06 |
| R38 安全与预算 | B01、B03、B09 | 所有批次 |
| R39 验收和人工复审 | B10 | 所有批次 |

## 5. 里程碑与退出条件

**M0 / B00：** 基线 SHA、现有可用测试、私有评估存储位置与权限记录清楚；不把无法获取的真实数据编成 gold。

**M1 / B01–B02：** 旧新版本不能反复领取同一完成任务；只改 why/status 不确认标签；阅读增强不再重复返回原文；备注不重抓；错误类别明确。P0 未通过不得推进默认生产语义。

**M2 / B03–B05：** 证据/问题/答案可恢复；Decide/Resolve 可在禁止网络时重放；v2 标签语义、v1 投影、失效矩阵、幂等和人工覆盖全有契约测试。

**M3 / B06–B07：** Web 与 Android 能显式确认/拒绝/清空/恢复自动；旧客户端不破坏 v2 数据；三种状态与分面导出准确；真实浏览器和 Android 本地门禁有证据。

**M4 / B08–B09：** 工程评估可离线复现；真实 gold/付费授权齐全才填写模型质量结论；扩展实现可关闭、有降级和预算，重排不改变候选权限及过滤条件。

**M5 / B10：** 逐项追踪到实现提交与测试，双仓 SHA 锁定，迁移/回滚演练完成，所有未验证/阻塞项明确。等待所有者及后续审查，不自动合并或部署。

完成分三列记录：`engineering_done`、`quality_verified`、`deployment_authorized`。不能用“未授权部署”掩盖代码未完成，也不能用“代码通过模拟测试”代替真实质量验证。

## 6. 外部参考与复核要求

权威链接收录于 REVIEW；项目已有 `.agents/skills/typesafe-ai/SKILL.md`。本轮整理时网页工具重新访问 TypeSafe 页面未成功，所以这里不宣称再次验证了当前线上 API；对语言、上下文限制、模型 ID、Score 返回结构、confidence 含义、HTTP 错误码，应在 B04/B08 实施时重新读取官方文档并保存日期/版本/脱敏 fixture。不得凭空补 API 字段或把既有说明视为永久契约。

## 7. 当前交付内容与边界

本组 PR 初始只提交审查、执行计划、验收规范和工作分解；没有提交功能实现。DeepSeek 需要在各 head branch 补代码、测试和证据，把 Draft 转为 Ready 的决定留给所有者。全量分析已经归档，但任何事实因基线变化不再成立，都要附源码和测试说明后更新，不能盲目照抄旧结论。
