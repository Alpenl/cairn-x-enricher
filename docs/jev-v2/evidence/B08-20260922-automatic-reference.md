# B08 / 2026-09-22 自动参考基准与首次真实训练评估

总控 [E #10](https://github.com/Alpenl/cairn-x-enricher/issues/10)，本项 [E #14](https://github.com/Alpenl/cairn-x-enricher/issues/14)，最终验收 [E #16](https://github.com/Alpenl/cairn-x-enricher/issues/16)。按[所有者授权](../AUTHORIZATION-20260922.md)，不再等待人工标注；采用版本化的自动参考数据。**本轮局部工程验证通过，训练诊断完成；B08 整体与留出集质量仍未完成，不能晋升。**

## 固定版本与样本

- Share 伴随代码：`136faae21a8bddf101fb67977e2c4fabb2b98569`，本轮未修改 Share。
- Enricher 参考冻结、scorer/live runner：`800d7e58f7ecd39ca56049fd641d2aa460efa9c7`，提交发生在第一次付费调用之前。
- Enricher 本轮最终受测代码：`ac60d9a34968e3e2fc2087305f6332f038116f92`，增加真实响应边界修复和零网络恢复。
- 模型 requested/resolved 均为 `jev-1.13.0`；spec `classify-80156c157660` / `2a39cb299aa0bf916bb4ac9642c1e515dd07f35883a61da403b4c53c05ce4d26`；policy `jev-policy-v2`，`calibrated=false`，未改生产阈值。
- [参考数据及生成说明](../../../experiments/classification/reference-v1/README.md)：40 个合成场景组、每组六种语言/结构变体，共 240 条；包含中英混合、长文本、外链、作者续帖、引用、词表外、多主题、图片缺失等。长文本使用重复段落作长度压力，不冒充自然长文章。
- 分组种子 `20260922`：train 42 条 / 7 组、dev 36 条 / 6 组、holdout 162 条 / 27 组。实际生产 taxonomy；自动参考答案先于预测冻结，unknown 与合法 none 分开，不猜用户立场；参考和材料哈希、跨组/跨集合/重复来源均校验。
- 训练参考 hash：`ea14264cbaabcedd13a328bfef6473cf31a18827ca0285b27c8047150370b414`。本轮未调用 dev 或 holdout。

## 实际运行与缺陷修复

先执行 1 条烟雾调用，再执行训练集。第 24 条训练响应 HTTP 200，但 Choice 概率总和为 0.99，二进制浮点表示可能使偏差略大于已有的 0.01 容差，导致客户端错误停止。保存响应未改动；修复仅给原有容差边界增加 `1e-12` 算术余量，没有扩大到其他百分点、归一化响应或放宽缺项/非法值检查。

离线恢复工具使用无网络回退的内存 transport，将原响应重新交给生产 `Evaluate` / `Decide`。验证完整 reference、原始请求字节、wire state、问题集、模型、spec、source hash 与 reserved/finished 日志。24 条全部恢复，新增调用 0；找回第 24 条实际 usage 后不存在未知用量。随后只调用导出的 18 条未尝试样本，再离线合并为原完整 train hash。原始失败、调用和响应保留，既不覆盖失败记录，也不把恢复计成新推断。重复样本、身份不匹配、截断或不完整日志拒绝恢复；失败样本不会被塞回未尝试清单自动重试。

真实请求沿生产有界 evidence 与 question 构造，29 个问题/次；requested/resolved/spec/实际 state、原始 typed judgments、每次 usage/耗时/重试/缓存/预算记录在私有产物。每次尝试先持久化 65,536 输入 token 预留，基于当日官方 64k 输入上下文上限；不把字符数当 token，也不退还失败预留。普通 verify/CI 不读取凭证或调用付费模型。

## 训练诊断与成本

[机器报告、分语言用量、分维/标签指标、混淆和组 bootstrap 区间](logs/20260922-evaluation/training-summary.json)。计分排除 unknown；单选可接受集合、多标签必需集合、正确空、缺预测、弃权分别处理；六维错误接受均计入错误率，不能靠全弃权获得通过。

| 训练指标 | 实测 |
|---|---:|
| 完整训练样本 / 独立组 | 42 / 7 |
| 已接受标签 micro precision | 94.54% |
| 正标签 micro recall | 82.38% |
| 已接受标签错误率 | 5.46% |
| 已决定字段覆盖率 | 61.43% |
| 待复核字段 / 样本 | 1.93 |
| Topic Brier / ECE | 0.02087 / 0.05766 |
| 真实训练输入 / 输出 token | 270,731 / 27,482 |
| 单次训练请求 p50 / p95 | 409 / 653 ms |

待复核字段数是策略诊断，不表示必须安排人工标注。训练仅 7 个独立组，冻结门禁要求至少 20 组与 20 条参考，因此 `inconclusive=true`、`promote=false`。这不是留出集结果，也不代表真实用户检索效果。健康场景额外接受 life、部分 law/eval 外链样本主题弃权等错误保留在原结果中，没有据此改参考答案。

合计 **43 次付费请求**（1 次烟雾重复 + 42 条训练；无自动 retry/cache）。输入 token **277,046**，预留总额 **2,818,048**。按 [TypeSafe Models](https://docs.typesafe.ai/models) 2026-09-22 公布的输入 $0.042/百万 token、输出免费计算，用量费用估计 **$0.011635932**，预留上界估计 **$0.118358016**；这不是对账单金额。烟雾重复计入费用但不计入训练质量。

原始 plan/calls/wire/result/recovery 位于本机 `~/.local/share/cairn/evaluations/20260922-reference-v1-*`，目录 0700、文件 0600；未提交原始请求/响应、API key 或私人收藏。公开仅合成参考、聚合报告和工程日志。

## 可重复验证

代码 `ac60d9a34968e3e2fc2087305f6332f038116f92`：

| 命令 / 路径 | 结果 |
|---|---|
| `make verify` | exit 0；vet、完整 golangci-lint、全包 race、前端 73 checks、生产 build |
| `make test-ablation` | exit 0；包括参考生成、离线恢复与评估工具 |
| `go test -race ./internal/classify ./internal/evaluation ./experiments/classification/main` | exit 0 |
| 冻结生成回归 | 两次生成与已提交四份冻结文件逐字节一致；240 条、跨集合无组/来源重复 |
| 恢复回归 | 零网络、旧失败保留、未尝试导出、重复/身份/乱序/缺日志拒绝；失败 paid call 不自动重试 |
| 真实训练 | 24 + 18 条，离线恢复/合并 42 条；完整 source/reference/model/spec 一致 |

[完整验证日志](logs/20260922-evaluation/enricher-verify.log)。最初完整检查发现恢复测试的三处路径未 `Clean`，已经修正后重跑全部通过，未屏蔽规则。Share 本轮未重测；其固定 HEAD CI [35708153209](https://github.com/Alpenl/cairn-share/actions/runs/35708153209) 是之前 R3-09 的 Worker/Android 构建与单测成绩，不是设备验收。本轮 Enricher 远端 CI 待推送后按最新 HEAD 在 Issue #16 登记。

```sh
# 完全离线：冻结生成由 test-ablation 验证；已有私有响应重放与合并
 go run ./experiments/classification/main \
   -dataset experiments/classification/reference-v1/frozen/train.json \
   -recover-from /private/train-first,/private/train-remaining \
   -output /private/new-recovery

# 显式付费命令先 dry-run；仅未尝试样本，预算不得小于样本数
 go run ./experiments/classification/main \
   -dataset /private/recovery/unattempted.json \
   -catalog experiments/classification/reference-v1/taxonomy.json \
   -live -dry-run -max-samples 18 -max-calls 18 -max-tokens 1179648
```

## 原任务映射与下一步

本轮为 B08-T01–T06、T07/T11–T14 提供局部工具与真实训练证据；不勾整个 B08。B08-T05 仍需时间切分/holdout 访问记录；T07 仍需 reliability/Score 及 confidence/margin 实验；T08 仍需生产完整 policy 的风险权重、适用范围、model/spec 绑定拟合与同 raw 重放；T09 仍需真实受控消融；T10 仍需固定候选找回评估；T13 仍需包括工程不变量/成本的完整 gate 与冻结候选后的 holdout。T14 仍需最终报告。生产导出/复用 R3-10、R3-12 数据身份等也未因本轮 live runner 通过而完成。

自动参考授权解决了人工标注前置条件，没有取消原 R/B/SC 范围。尚未部署、合并或启用扩展 flag。整体验收继续在 #16。
