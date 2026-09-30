# B04/B08 客观用途与个人立场隔离 · 2026-09-23

本批修复实际缺陷：没有任何明确用户立场输入时，模型的高概率 `contra` 会被 Classify 和默认离线 Decide 接受；Worker 旧完成、v2 automatic 和 accepted assessment 也允许这种个人立场写入。修复覆盖问题候选、版本化 policy 与真实写入口，**保留人工选择及历史审计，不宣称分类质量验收通过**。

受测代码：Share `9571445910a5426a0a74654bdd59a9223fb158bc`，Enricher `8dced002f1f6a96e0b98d7f17c5907d64a4711b8`。推进 B04-T05/T10/T11/T13/T15 和 B08-T09 的客观/个人边界工程部分，主责 #12，联动 Share #29 / #14，统一验收 #16；完整原任务未关闭。

## 生产行为与身份

- `contra` 是既有词表保留的个人立场 ID。新客观 use 问题只依据来源材料判断潜在用途，排除该候选，不再要求不存在的收藏备注。实际请求在 Note 有无/内容变化时逐字节相同，其他问题不变。只剩个人词条、没有可用客观用途的词表在创建客户端时拒绝，不发无效 Choice 请求。
- 默认 policy 升级为 **`jev-policy-v3`**，显式 **`block_personal_use=true`**；接受/拒绝阈值未改变，仍未校准。旧 raw 的 contra 在新 policy 下弃权、有效 use 留空；候选、概率和“需要明确人工输入”的理由保留在审计字段，原 raw 不变。
- 显式载入旧 `jev-policy-v2`（未含 guard 字段）的离线审计仍恢复旧输出，不偷偷改写历史政策语义。它不是可重新写入的安全建议。v2 加 guard、v3 去 guard 都被校验拒绝，避免相同版本名表达不同语义；旧 JSON 无新增 false 字段。
- 当前消费者协商能力同步声明 v3。固定参考词表编译的新 spec 为 **`classify-736e775b96c2`**，hash **`30a331056f34cd2ad55927fd281fd57cb016f81a7f599bba5ac3a4110577a67e`**。问题语义与 policy 分别有新身份，不复用旧回答冒充新问题质量结果。
- Worker 对新模型 classification 的 use、v2 automatic use，以及 use/uses 维度 accepted assessment 的 value/term/candidate 执行守卫；独立 decision/replay 写入口同样拒绝。旧 consumer、旧 policy 或只篡改 assessment 都不能把个人立场重新写入自动投影。拒绝发生在写事务前，不生成 run、decision、引用或 projection，也不完成任务。
- `validateSelection` / 人工 curation/override 与历史 stored 读取保留原行为：用户明确保存的 contra 不删除，后续正常自动分类/重放仍不覆盖它。既有历史模型记录保持可读而非偷偷清理。守卫针对当前受支持词表中的保留 ID，不把任意文本关键词当作用户立场。

[规范与启用边界](../04-judgment-policy.md)已更新。无新迁移、无数据清理、无生产 target 切换。启用时需兼容 Worker 守卫与审核后的新 spec/policy/model；旧 consumer 的不安全 contra 完成会被拒绝，目标不匹配沿用配置暂停，不伪称兼容领取任务。

## 失败对照与实际验证

| 范围 | 本批证据 |
|---|---|
| Go 旧实现 | [2 个明确失败](logs/20260923-personal-intent/b08-intent-baseline.log)：实际 provider HTTP 分类和默认 raw 重放都产出 contra |
| Worker 旧实现 | [3 个明确失败](logs/20260923-personal-intent/b08-intent-worker-baseline.log)：旧分类完成、v2 决定值、v2 accepted assessment 实际返回 200，本应 400；同批人工保留用例通过 |
| Go 当前 | [最终 make verify](logs/20260923-personal-intent/b08-intent-verify-final.log)：lint、全包 race/coverage、73 前端检查、构建通过；新回归验证新候选/Note 隔离、默认弃权、历史 raw/旧政策不变、版本守卫和无客观候选时启动拒绝 |
| Worker 当前 | [210 项全量测试](logs/20260923-personal-intent/b08-intent-worker-final.log)通过；[typecheck / deploy:dry-run](logs/20260923-personal-intent/b08-intent-worker-all.log)通过，未部署；补充[历史读取检查](logs/20260923-personal-intent/b08-intent-history-worker.log)与类型检查通过 |
| 实际服务 | [10 个独立 Worker/D1/R2 + Go 场景](logs/20260923-personal-intent/b08-intent-real-services.log)通过，不依赖内存存储替代服务 |

新增第 10 个实际服务场景运行真实 Go classifier → HTTP → Worker/D1：同一合法模型结果分别被篡改 legacy use、automatic use、accepted assessment，三次提交都以 contract 错误拒绝，run 数保持 0；随后原合法结果完成成功。人工明确 accept contra 后，不安全重放被拒绝且原 decision ID 不变；合法重放仍成功，有效 use 保持人工 contra。全场景仅 **1 次本地模型夹具调用**，拒绝/提交重试/离线重放没有增加调用。其余九场景覆盖生命周期、竞争、重命名、来源快照、实体、问题复用、多运行引用、补证据所有权恢复和真实筛选。

开发过程中的编译 import、lint、旧能力断言和夹具类型错误均保留在日志中，未关闭规则或删除断言。新增实际服务初次失败是夹具使用 pinned 请求模型，而共用目标 helper 固定请求 alias；对齐为相同 alias 后通过，没有放宽生产握手。最终全十场景再次通过。Worker 完整日志中的 `deleteAllDurableObjects` 清理诊断也原样保留，测试命令退出 0。

[Share CI 35769767080](https://github.com/Alpenl/cairn-share/actions/runs/35769767080) 与 [Enricher CI 35769765478](https://github.com/Alpenl/cairn-x-enricher/actions/runs/35769765478) 均 SUCCESS。完整 [Share 步骤记录](logs/20260923-personal-intent/cairn-share-ci.json) / [Enricher 步骤记录](logs/20260923-personal-intent/cairn-x-enricher-ci.json)绑定上述精确源码，包含 Worker、Android 常规门禁、Go lint/race 和多架构容器构建。

本批没有 Android 源码变更，也没有重新运行设备 instrumentation；远端普通 Android 单测/构建不冒充设备验收。

## 历史实验、成本与剩余范围

本批 **0 次新增付费调用**，累计仍 **127 次**。新增零调用 [spec/预算计划](logs/20260923-personal-intent/b08-intent-dry-run.json)只是 dry-run，不是已执行实验。实际服务中模型均为本地受控 fixture。

此前来源角色完整 spec 保存为 [不可变历史对象](../../../experiments/classification/reference-v1/source-roles-spec.json)，原角色消融测试比较固定历史 spec 和固定干预前缀；新测试另验证当前问题只有 use 发生变化。原 baseline/spec、train/dev/holdout/manifest 参考未改，四文件完整性仍通过；CI 的字节/schema/泄漏读取不计质量评分。dev/holdout 没有新增模型、拟合或质量使用。

这是工程边界修复，包含候选、措辞、policy 和服务端约束，**不称作已执行的单变量质量实验**。旧 42 条角色实验的负结果与 0/336 policy 搜索结果保留；新问题 spec 尚需独立质量评估。载体互斥边界、潜在用途参考范围、概率/Score/置信度、完整消融、固定候选检索、dev 和冻结后 holdout 继续执行。

原 126 B、全部 R/SC、B07/B09/B10 和独立复审范围不缩减。无需人工标注，自动参考不冒充人工 gold。两 PR 保持 Draft；未合并、部署、执行生产迁移或关闭原 issues。
