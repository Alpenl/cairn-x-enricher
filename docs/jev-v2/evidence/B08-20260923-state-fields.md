# B08 实际输入字段名受控实验 · 2026-09-23

结论：修复了问题指令与实际 JSON 字段不一致的工程错误；训练质量有升有降，**不构成 B08 验收通过或 policy 晋升**。源码与事前协议冻结于 Enricher `97b9e3250bc0d23582a6bc7ed4155a62b69e57d0`。Share 沿用 `747e8830d4c387b691135fe96f3cf4f46b3a01ee`，无 Worker/Android 源码变更。

## 固定干预和实际请求核验

仅把共用材料指令中的两个字段名 `original_text` / `context_text` 改为实际发送的 `primary` / `context`。29 个问题的类型、题目、候选及判断标准均保持不变，生产 policy 仍为未校准的 `jev-policy-v2`，模型固定 `jev-1.13.0`。本批尚未改变 context 的角色描述和 use 中的收藏备注措辞，它们是后续独立因素。

[事前协议](../../../experiments/classification/reference-v1/STATE-FIELDS-ABLATION.md)及[零调用预算计划](../../../experiments/classification/reference-v1/state-fields-plan.json)先于真实调用提交并推送。旧 spec 为 `classify-80156c157660` / `2a39cb299aa0bf916bb4ac9642c1e515dd07f35883a61da403b4c53c05ce4d26`；新 spec 为 `classify-0b02fbfce85d` / `c760533cfd880ba0201f97d41c3a1d4d6856bce6c816feb30328d160a7cdead5`。

[离线对比脚本](logs/20260923-state-fields/b08-fields-compare.py)验证保存的真实请求：42 对 state 完全相同，逐一反向替换全部 **1,218 个 question instruction** 后，完整 request 与旧请求相同。参考、样本材料、模型和 policy 相同，全部一次尝试、无重试、无缓存。新请求身份与旧身份分别保留。

历史完整 [baseline spec](../../../experiments/classification/reference-v1/baseline-spec.json)独立保存。referencegen 校验历史 ID/hash/taxonomy，并使用它生成历史参考；四个冻结文件逐字节不变，参考 hash 仍为 `ea14264cbaabcedd13a328bfef6473cf31a18827ca0285b27c8047150370b414`。没有为适应新指令改写旧答案或 manifest。

## 实际训练结果

42 条训练样本来自 **7 个独立自动编写场景组**；翻译和材料变体不作为新的独立组。固定原始默认阈值进行主对比，本批未拟合新 policy。

| 指标 | 旧字段名 | 实际字段名 |
|---|---:|---:|
| micro precision | 94.54% | 94.92% |
| micro recall | 82.38% | 89.05% |
| macro precision | 92.20% | 91.82% |
| macro recall | 78.51% | 84.79% |
| accepted error | 5.46% | 5.08% |
| 完整字段覆盖率 | 61.43% | 60.00% |
| 每条待复核字段 | 1.929 | 2.000 |
| 主题 Brier | 0.02087 | 0.01852 |
| 主题 ECE | 0.05766 | 0.05511 |

Brier/ECE 只覆盖主题概率，不是六维总体校准结果。完整字段覆盖下降与复核负担上升同样保留。

| 维度 F1 | 旧字段名 | 实际字段名 | 已知参考数 |
|---|---:|---:|---:|
| 主题 | 0.8000 | 0.9091 | 42 |
| 功能 | 0.8788 | 1.0000 | 36 |
| 载体 | 0.9756 | 0.9091 | 42 |
| 潜在用途 | 0.6000 | 0.5714 | 12 |
| form | 0.9114 | 0.9114 | 42 |
| use | 0.9091 | 0.9714 | 36 |

[完整聚合对比](logs/20260923-state-fields/comparison.json)和[全部条件/分组报告](logs/20260923-state-fields/reports.json)包含中文、英文、混合语言，以及 primary-only、quoted、author_continuation、external_article。载体退步集中于 7 条 external_article 样本：该组 F1 从 0.8333 降至 0；其余上下文组载体 F1 均保持 1。结构化决定显示原先 5 条接受正确的 external_article 变为弃权，另 2 条仍弃权；未新增错误载体接受。该外部文章组同时全为中文，语言与上下文因素混杂，不能归因于某一个因素。[事后结构化决定转移](logs/20260923-state-fields/carrier-transitions.json)保留接受/弃权变化，不公开原文。

报告中的区间是各条件内部按 group bootstrap 的区间，**不是配对差值置信区间**。只有 7 组，未重复运行估计供应商/时间波动，自动参考范围偏差也不包含在区间内；不能从此次总分变化推断稳定或显著因果收益。全部报告 `promote=false`；训练组数不足，真实质量未验收。

## 调用、成本和访问边界

本批新增 **42 次**真实训练调用，全部完成、usage 已知。输入 **268,295 tokens**，预算预留 **2,752,512 tokens**；p50 **371 ms**、p95 **446 ms**，仅为本批实测延迟。历史 43 次加本批 42 次，累计 **85 次**。未重复请求已经保存的响应，离线对比不请求模型。

按 2026-09-23 核对的 [TypeSafe 模型价格](https://docs.typesafe.ai/models)，输入每百万 tokens $0.042，输出免费；本批输入费用估计 **$0.01126839**，预留上限费用估计 **$0.115605504**。这是价格估算，不是账单。[价格与预算](logs/20260923-state-fields/b08-fields-price.json)、[调用日志](logs/20260923-state-fields/b08-fields-live-train.log)、[对比日志](logs/20260923-state-fields/b08-fields-comparison.log)归档。

dev/holdout 未用于模型调用、拟合或质量评分。现有 referencegen CI 会读取这些冻结文件作字节、schema 与泄漏完整性检查，因此这里不声称从未读取其文件。原始 wire/calls/result 和样本材料仅保留在本地私有 0700 目录、0600 文件；公开内容仅协议、工程验证、聚合结果和安全脚本。

## 工程验证

旧源码的 [失败对照](logs/20260923-state-fields/b08-fields-baseline.log)暴露 29 个字段指令不匹配及 spec 身份未变化；修复后验证真实 BuildProviderRequest 和历史问题的精确两字段差异。历史 spec 被修改时 referencegen 必须拒绝且不生成输出，四个冻结文件完整性检查通过。

首次和第二次完整检查因测试文件写入方式触发 G703 失败，改用限定根目录的 `os.OpenRoot.WriteFile` 后通过，未屏蔽 lint。[首次](logs/20260923-state-fields/b08-fields-verify-first.log)、[第二次](logs/20260923-state-fields/b08-fields-verify-second.log)、[最终 make verify](logs/20260923-state-fields/b08-fields-verify-third.log)日志均保留。最终 lint、全包 race/coverage、73 前端检查及构建通过。

[9 个独立实际服务场景](logs/20260923-state-fields/b08-fields-real-services.log)通过：生命周期、版本竞争、重命名语义、source checkpoint 与有界读取、实体、问题级复用、决定引用、补材料所有权恢复、实际 dashboard 筛选；实际 Worker/D1/R2 与 Go HTTP 服务运行，模型使用合同夹具。真实付费模型结果由上一节另行记录。本批未重复 Android 设备测试。

[源码 CI 35765392155](https://github.com/Alpenl/cairn-x-enricher/actions/runs/35765392155) SUCCESS，确切 SHA 和步骤见[运行记录](logs/20260923-state-fields/remote-ci.json)。

## 后续与完整范围

继续核对来源角色、收藏备注隔离、潜在用途定义与自动参考范围，分别冻结干预后验证。不得事后修改 v1 参考或降低覆盖门槛来强行通过；旧六维阈值搜索 0/336 合格结论保留。完整可靠性分箱/Score、confidence/margin 增益、逐变量消融、固定候选检索、成本对照、dev 和 policy 先冻结后 holdout 仍待完成。

无需人工标注，自动参考不冒充人类偏好 gold。原 126 B、全部 R/SC、B07/B09/B10 和独立复审范围不缩减。总控 #10 / 验收 #16 仍在执行；PR 保持 Draft，未合并、部署、执行生产迁移或关闭原 issues。
