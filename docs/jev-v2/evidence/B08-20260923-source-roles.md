# B08 来源角色描述受控实验 · 2026-09-23

结论：修正了把所有 context 称为“引用或评论”的合同错误；**没有获得总体质量改善，完整 B08 仍未通过验收**。E 源码及事前协议固定于 `315f7d976726dbc5f91a81f25b1ff835bda8a085`；S 沿用 `747e8830d4c387b691135fe96f3cf4f46b3a01ee`，无 Share 源码变更。

## 干预和固定身份

共享材料指令明确区分 `author_continuation`、`quoted`、`external_article`、`third_party`、`legacy_unknown`，允许上下文补充可观察的来源结构，同时禁止把第三方主张归给作者或让上下文替代原帖主题。实际 state、所有问题的具体题意/类型/criteria、catalog、model 和 policy 均不变；收藏备注措辞、潜在用途定义、Score 未在本批调整。

[事前协议](../../../experiments/classification/reference-v1/SOURCE-ROLES-ABLATION.md)、[预算计划](../../../experiments/classification/reference-v1/source-roles-plan.json)、[精确干预](../../../experiments/classification/reference-v1/source-roles-intervention.json)和[离线比较程序](../../../experiments/classification/reference-v1/compare-source-roles.py)在新推断前提交并推送。

- 基线：上一轮已保存的 42 条字段名修复结果，`classify-0b02fbfce85d` / `c760533cfd880ba0201f97d41c3a1d4d6856bce6c816feb30328d160a7cdead5`，没有重跑基线。
- 变体：`classify-b915a8cff237` / `6f3b685f646a8aed546b93d759c4acf70552f7a4e2a0c4efac2748a2b6b1d137`。
- 模型 `jev-1.13.0`，未校准默认 `jev-policy-v2`；参考 hash `ea14264cbaabcedd13a328bfef6473cf31a18827ca0285b27c8047150370b414`。
- 实际 42 对请求逐一验证：state 完全相同，1,218 个 question 的共享角色前缀反向替换后，完整请求与保存的基线相同；无其他请求变量变化。

历史字段名实验的两个完整 spec 独立保存；其测试比较固定历史对象。新测试另验证当前生产 spec 与字段名基线仅共享角色规则不同，避免未来合法语义变更迫使历史实验改写。四个冻结参考文件逐字节不变。

## 结果：负结果完整保留

42 条训练来自 7 个自动编写场景组，翻译/变体不算新的独立样本组。固定默认 policy 做主比较，本批不拟合新阈值。

| 指标 | 字段名修复基线 | 来源角色变体 |
|---|---:|---:|
| micro precision | 94.92% | 93.37% |
| micro recall | 89.05% | 87.14% |
| macro precision | 91.82% | 89.83% |
| macro recall | 84.79% | 83.00% |
| accepted error | 5.08% | 6.63% |
| 完整字段覆盖 | 60.00% | 61.43% |
| 每条复核字段 | 2.000 | 1.929 |
| 主题 Brier | 0.01852 | 0.01825 |
| 主题 ECE | 0.05511 | 0.05868 |

| 维度 F1 | 基线 | 变体 | 已知参考数 |
|---|---:|---:|---:|
| topics | 0.9091 | 0.8966 | 42 |
| content_functions | 1.0000 | 1.0000 | 36 |
| carriers | 0.9091 | 0.9091 | 42 |
| affordances | 0.5714 | 0.5455 | 12 |
| form | 0.9114 | 0.8889 | 42 |
| use | 0.9714 | 0.9254 | 36 |

[完整聚合对比](logs/20260923-source-roles/comparison.json)、[全部语言/角色分组报告](logs/20260923-source-roles/reports.json)保留。覆盖和复核负担略改善，precision/recall、accepted error、潜在用途/form/use 的质量变差。Brier 略降而 ECE 上升，两者只评估主题概率，不能称全维校准通过。

7 条 external_article 的载体仍全部弃权，F1 仍为 0；其他上下文组载体 F1 为 1。该组全为中文，语言与来源角色混杂，不能单独归因。只有 7 个独立组，全部报告 `promote=false`；各条件内部 group-bootstrap 区间不是配对差值区间，也未估计供应商/时间波动或自动参考范围偏差。不能从一次训练结果宣称稳定因果收益或生产质量通过。

## 事后诊断：载体词条边界

本段是看到结果后对已保存响应的零调用诊断，不是事前假设验证。当前产品合同规定载体单值；训练 catalog 的“单帖”定义为“单个帖子，没有作者续帖”，没有排除同时带外链文章的单帖；“外链长文”也可能满足前一条件。其互斥边界需要独立澄清，不能直接因为低分把载体全改多选。

[载体概率聚合](logs/20260923-source-roles/b08-roles-carrier-diagnostic.json)显示该组基线 single 中位数 0.50、external_article 0.46；角色变体为 0.52 和 0.44，两个选项分摊了概率。这与定义重叠相符，但不是因果证明。后续词条边界变更须独立冻结 spec/语义和评估，不能事后改 v1 标签、降低门槛或重跑直到通过。

## 调用、成本和样本访问

本批新增 **42 次**模型调用，全部完成、一次尝试、无 retry/cache、usage 已知；历史 85 加本批 42，累计 **127 次**。新增输入 **432,725 tokens**，预留 **2,752,512 tokens**；p50 **356 ms**、p95 **458 ms**。[调用记录](logs/20260923-source-roles/b08-roles-live-train.log)和[比较日志](logs/20260923-source-roles/b08-roles-comparison.log)保留。

相同 42 条输入在基线耗费 268,295 tokens，角色描述增加 164,430 tokens（约 61.29%），未换来总体质量收益。按 2026-09-23 核对的 [官方价格](https://docs.typesafe.ai/models) $0.042/M input、output 免费，本批输入价格估计 **$0.01817445**，预算上限估计 **$0.115605504**，均不是账单。延迟只是本批实测，不宣称长指令更快。

模型调用、拟合和质量评分仅使用 train；dev/holdout 仍未用于这些用途。现有 CI 对冻结文件作字节/schema/泄漏完整性读取，不是质量评估。原始请求/响应/result 仅保留在本地 0700 私有目录、0600 文件；公开只有安全聚合与工程证据。

## 工程验证

- [旧实现失败对照](logs/20260923-source-roles/b08-roles-baseline.log)：29 个实际问题都缺少五类角色解释并错误泛称引用/评论，旧身份未变化亦被拒绝。新真实 provider body 测试覆盖全部角色、块顺序与个人字段隔离；角色差异测试固定全部其他问题内容。
- [分类及 referencegen 测试](logs/20260923-source-roles/b08-roles-targeted.log)、[完整 make verify](logs/20260923-source-roles/b08-roles-verify-first.log)通过：lint、全包 race/coverage、73 前端检查、构建。普通测试零付费。
- [比较器负例](logs/20260923-source-roles/b08-roles-comparator-negative.log)验证把旧结果冒充新变体时，在写输出或评分前拒绝，不使用错误实验身份得出结论。
- [9 个实际服务场景](logs/20260923-source-roles/b08-roles-real-services.log)通过，运行真实 Worker/D1/R2、Go processor/client/HTTP 与存读恢复，模型使用受控合同夹具；真实付费模型的结果单独列于上文。本批未重复 Android 设备测试。
- [源码 CI 35767391956](https://github.com/Alpenl/cairn-x-enricher/actions/runs/35767391956) SUCCESS，确切源码 SHA、lint/race 和容器构建步骤见[运行记录](logs/20260923-source-roles/remote-ci.json)。

## 未完成范围

工程修复不自动批准新 spec 生产启用。仍需独立处理收藏备注语义、载体互斥边界、潜在用途/自动参考范围；完整概率/Score 校准、confidence/margin 增益、其他单因素消融、固定候选检索、成本对照、dev 以及先冻结 policy 再访问 holdout 仍在范围。

无需人工标注，自动参考不冒充人类偏好 gold；旧 policy 搜索 0/336 合格的结果保留。原 126 B、所有 R/SC、B07/B09/B10 和独立复审不缩减。总控 #10 / 验收 #16 继续执行；PR 保持 Draft，未合并、部署、执行生产迁移或关闭原 issues。
