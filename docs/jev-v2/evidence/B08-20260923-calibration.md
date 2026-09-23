# B08 六维概率诊断、有序 Score 向量与输入守卫 · 2026-09-23

本批新增从已验证原始判断生成六维概率可靠性报告的离线命令，补上此前只有主题 Brier/ECE 总数的缺口。使用上一轮已保存的 42 条训练响应，**零新增模型调用**。潜在用途 ECE 约 **0.366**、可用参考仅 2 个独立组；confidence 曲线未建立独立增益，**没有选择新阈值、没有宣称已校准或通过质量验收**。

Enricher 受测源码 **`b820461cbd488ea356aaf2e6077c765ec6349383`**；配套 Share **`f7ed68e8831a7a4f57fcc769bc23334f409f47bb`**（本批未改）。推进 B08-T07/T11/T14 与 B04 typed 合同工程部分，完整 B08/原范围继续。当前问题 spec `classify-ffb0edb18cb5`、默认 v3 policy 及所有阈值保持原值。

## 实现与证据边界

[诊断协议与公式](../../../experiments/classification/reference-v1/CALIBRATION.md)及[固定配置](../../../experiments/classification/reference-v1/calibration-v1.json)随源码先提交/推送，再执行本次分析。这是训练诊断预定义，不是整个训练集从未被看过的独立实验声明。

- 新 `-calibration` 入口与现有拟合共用 journal 装配及 `PreparePolicyReplay`：验证样本/参考、输入材料、完整 typed 答案、spec/问题/model/batch 和实际 bounded wire-state/hash。内置 transport 不能联网。未知/缺失/错误 journal 拒绝，不凭预测标签重建原始分布。
- 导出每个问题及六个维度的 Brier、ECE、所有可靠性分箱、观测数与独立组数。未知参考跳过；多个可接受 Choice 值不凭空均分为真值，单独记录并排除 one-hot 校准。参考正标签不在候选中拒绝。合法空 Choice 对应 none。
- 二元 Noul Brier 用 `[0,1]` 形式；Choice 对所有类别求平方误差和，理想范围 `[0,2]`。两种量尺不求总平均。Choice 可靠性检查被选选项的概率与正确性，不把 confidence 当正确概率，也不假设 provider 总选 argmax。原始舍入概率不归一化改写。
- 固定 10 个等宽箱，最后一箱包含 1；每箱记录数量、正结果、平均概率及观察频率。空箱均值/null，零观测 Brier/ECE=null，不能表现为完美 0。现有主题总指标和门槛不变。
- 选择性风险从**实际生产 Decide**开始，包括 none 的既有接受行为，分别增加 chosen probability / margin / confidence 截断；返回接受数/错误数/coverage/缺失特征。没有接受记录时错误率=null。分母是已知且无歧义的 Choice 观测，不能冒充全收藏字段覆盖。
- 本探索入口只接受 train/dev；holdout 在读取 replay 文件前拒绝，与 live/recovery/dry-run/gate/fit 混用也拒绝。输出必须是新目录，0700/0600，附配置/journal/catalog/dataset/reference hashes；不覆盖既有产物。最终冻结 holdout 验证仍单独完成。

## 共享 typed 输入修复

[原验证失败对照](logs/20260923-calibration/calibration-input-baseline.log)确实接受了 confidence **-0.01、1.01、NaN、+Inf**，以及 typed Score **NaN**。现在共享 validator 拒绝非有限/越界 confidence 和非有限 Score；旧省略 confidence 的响应继续可读，不编造缺失值。

实际 HTTP fixture 另验证合法 JSON 中 -0.1 / 1.1 的 confidence 产生明确 contract 错误。NaN Score 是直接 typed 对象边界测试，**不声称 JSON 线上传输可表示 NaN**。题目语义和 policy 未改变，因此不生成新 spec，也没有重跑推断。

## 42 条已保存训练响应的结果

先从上一轮 carrier 候选的保存 wire 纯离线恢复到新私有目录，恢复后的 dataset 与原结果逐项完全相同，42 条 metadata-v1 完整，0 新调用，[校验日志](logs/20260923-calibration/calibration-recovery-verified.log)。输入模型 `jev-1.13.0`，参考 hash `ea14264cbaabcedd13a328bfef6473cf31a18827ca0285b27c8047150370b414`。

[完整聚合 JSON](logs/20260923-calibration/calibration.json)保留 29 个问题与全部分箱、6 个维度、全部风险曲线；[输入绑定](logs/20260923-calibration/inputs.json)与[实际命令结果](logs/20260923-calibration/calibration-actual.log)可复核。

| 维度 | 概率观测数 | 已知独立组 | Brier | ECE |
|---|---:|---:|---:|---:|
| topics（二元） | 714 | 7 | 0.018588 | 0.056513 |
| content_functions（二元） | 180 | 6 | 0.045342 | 0.137222 |
| affordances（二元） | 48 | 2 | 0.264798 | 0.366042 |
| carriers（多类） | 42 | 7 | 0.017638 | 0.050000 |
| form（多类） | 42 | 7 | 0.162052 | 0.094286 |
| use（多类） | 12 | 2 | 0.003750 | 0.038333 |

观测数不是独立样本数：714 个 topic 概率来自 42 条变体/7 组。affordances 跳过 120 个未知参考的概率观测，已知 48 个仅来自 12 条/2 组。use 排除 24 个多可接受值和 6 个未知参考，表中很低的误差只代表余下 12 条/2 组。全部维度 inconclusive，不能据此称全维已校准；大量负标签、参考定义偏差及稀有标签覆盖仍需处理。

已知且无歧义 Choice 共 **96 个**：当前 policy 接受 **93**、错误 **3**，coverage 96.875%、接受错误率 3.226%。部分预定义曲线点如下，完整结果不隐藏：

| 在当前 policy 上附加的条件 | 接受 / 错误 | coverage | 接受错误率 |
|---|---:|---:|---:|
| 无额外条件 | 93 / 3 | 0.96875 | 0.03226 |
| margin ≥ 0.15 或 0.30 | 93 / 3 | 0.96875 | 0.03226 |
| confidence ≥ 0.65 | 92 / 3 | 0.95833 | 0.03261 |
| confidence ≥ 0.80 | 83 / 1 | 0.86458 | 0.01205 |
| confidence ≥ 0.90 | 79 / 0 | 0.82292 | 0 |
| chosen probability ≥ 0.90 | 81 / 0 | 0.84375 | 0 |
| margin ≥ 0.80 | 82 / 0 | 0.85417 | 0 |

在这个小训练子集，低 margin 门槛没有新增作用，confidence≥0.65 仅少接受一个正确结果。提高门槛能减少错误但损失覆盖；其他同分布特征也能达到零观察错误，不能把它们当相互独立的证据或证明 confidence 有额外增益。没有最优阈值选择、配对显著性/区间估计、乘分规则或自动晋升。这里的零错误不等于真实风险为零。

## Score：数学验证与尚未覆盖的质量范围

原 `TestIdenticalScoreMeanButDifferentDistributionIsRetained` 实际只比较两组 Bernoulli ECE，未使用 Score；已替换为真实 typed Score 向量。对三个有明确文字含义的有序级别，参考为中间级：`[0,1,0]` 与 `[0.5,0,0.5]` 均值和均值绝对误差相同，但规范化 RPS **0 / 0.25**、分布期望绝对距离 **0 / 1**。相邻/远距离点质量错误、rubric 顺序与无效输入也有测试。

`ScoreOrdinal` 保留级别顺序、完整分布和 confidence，计算返回均值的绝对误差、分布平均值及差异、期望绝对距离、K−1 累积阈值的规范化 RPS。它是有显式参考的数学 helper，**不替调用者证明来源/spec/参考 provenance**。

实际生产 spec 仍关闭 Score，当前数据 **0 个 Score 观测、ordinal_quality_evaluated=false**。有意义的产品 rubric、独立冻结的有序参考、Score-enabled 完整输入绑定及真实推断/必要性消融继续保留，不能用这些工程向量声称 Score 模型质量已经验收。

## 验证、成本与剩余范围

受测源码 CI [35776466591](https://github.com/Alpenl/cairn-x-enricher/actions/runs/35776466591) **SUCCESS**，exact head `b820461cbd488ea356aaf2e6077c765ec6349383`；[查询记录](logs/20260923-calibration/source-ci.json)。配套 Share 的既有源码 CI [35773803212](https://github.com/Alpenl/cairn-share/actions/runs/35773803212) 已通过，本批未改 Share。

- [完整 make verify](logs/20260923-calibration/calibration-verify-pass.log)通过：lint、全包 race/coverage、73 前端、构建，保留旧拟合验证。
- [12 个实际服务场景](logs/20260923-calibration/calibration-real-services.log)通过，使用固定 Share 的真实 Worker/D1/R2 与 Go 链路；本批无 Share 源码更改，无 Worker 全量或设备 instrumentation 重跑。
- [CLI 模式拒绝](logs/20260923-calibration/calibration-cli-mode.log)在读样本/凭据/联网前生效；命令测试覆盖私有输出、禁止覆盖、错误 journal、holdout 拒绝。
- 初次指标测试发现历史 raw dimension 为 `topic` 单数，修正为报告维度 topics，没有丢弃主题；[失败](logs/20260923-calibration/calibration-tests-first.log) / [通过](logs/20260923-calibration/calibration-tests-second.log)保留。完整门禁开发失败分别是 G304 测试路径规范化及 HTTP fixture 固定测试 key 不一致，均修复而非关闭规则或删断言；[首次 lint](logs/20260923-calibration/calibration-verify-first.log) / [HTTP fixture 失败](logs/20260923-calibration/calibration-verify-final.log)归档。

本批新增付费 **0**，累计仍 **211 次**。私有目录 `~/.local/share/cairn/evaluations/20260923-reference-v1-carrier-revalidated` 与 `.../20260923-reference-v1-probability-diagnostics`；公开仅汇总与合成测试日志。冻结参考和旧质量报告不变，无新 dev/holdout 模型、拟合或质量使用。

继续完成潜在用途参考范围、未知/混合载体、Score 实际质量、完整消融、固定候选检索、冻结后的最终质量评估与原全部 R/B/SC/F/R2/R3、B07/B09/B10 和独立复审。两 PR 保持 Draft；未合并、部署、执行生产迁移/target 切换或关闭原 issues。
