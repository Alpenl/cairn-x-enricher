# B08 / 评估、消融、校准与模型晋升

状态：待实现、真实质量未验证。总计划#3；base B06 #6，工具依赖B04与B03/B05的runs。覆盖R07/R10–R12/R17/R21/R23/R29/R34–R36/R39。

本批有两段：开始整个项目时先做数据授权/基线冻结；主实现完成后运行离线比较和获授权的真实评估。不能等新方案写完才挑一个有利基线。DeepSeek不是人工gold来源，模型输出也不能自动当真值。

## 目标

建立可离线重复的评估工具、合成工程fixture、私有人工数据入口、逐项消融与版本化晋升门禁。真实gold建议初始200–300条，但不足以证明所有稀有标签，必须报告有效样本数。没有真实标注或live授权时，完成工具/工程测试，`quality_verified=false`且不晋升；不得伪造指标或拿mock响应替代。

## 任务

- [ ] B08-T01 B01前准备：保存两仓基线SHA、v1 schema/policy、已有脱敏raw fixtures和可获得的真实运行引用。生成数据授权/存储说明；私人原文、备注、实体、标注不进公开Git。不存在的历史raw只能标missing，不用新模型重建后冒充旧输出。
- [ ] B08-T02 实现数据schema和校验器：sample_id、source revision/hash、语言/载体/长度/完整性、thread/near-duplicate group、gold按维度多标签与可接受集合、ambiguity、annotation provenance、retrieval query relevance。支持unknown与not_applicable，不强迫人给本来无适用项的标签。
- [ ] B08-T03 分层采样工具/清单：中文、英文、中英混合；短文/长文/外链/图片依赖；多主题、引用反驳、领域外、边界混淆。显式ID/授权本地导出，无自动全库抓取。公开只提供合成fixture及聚合统计，未经批准不上传私人gold。
- [ ] B08-T04 标注指南落实R03–R05语义，至少对困难样本做独立复核与争议记录；没有第二标注者就报告限制。明确区分内容功能/载体/潜在用途/用户真实意图，不能从原帖给用户stance标真值。旧隐式reviewed记录不自动成为正样本。
- [ ] B08-T05 先按thread/近重复分组，再固定train/dev/holdout划分与种子；支持时间切分观察漂移。验证同组不跨集合、相同snapshot不重复、同查询评估候选集锁定。holdout访问与调参记录可追踪，不能看完holdout再改阈值选最好版本。
- [ ] B08-T06 实现纯离线scorer：每标签/每维precision、recall、micro/macro、accepted错误率、coverage/abstention、review fields/bookmark、合法空处理；同时列confusion、样本数、零分母规则与置信区间。多标签不套用单分类accuracy；不能仅因大幅弃权让precision漂亮。
- [ ] B08-T07 支持概率校准评估（适用的Brier/ECE/reliability bins，记录分箱和小样本限制）、Score有序误差与分布信息。评估confidence/margin特征是否实际有增益，不默认乘分。样本稀少标签共享/分组阈值，避免过拟合。
- [ ] B08-T08 实现受控阈值搜索与policy导出：只在train/dev拟合；阈值、损失/风险权重、样本适用范围、模型/spec绑定版本化。policy更新用同raw runs重放；问题含义/实际state变更必须新推断，不能比较不匹配的数据。
- [ ] B08-T09 构建消融矩阵：v1、仅局部弃权、仅词条边界、仅单多选维度调整、必要Score、中/英文问题规格、来源角色/备注隔离。逐步改变并保留共同可比较子集，v1/v2维度不等时用明确mapping或独立任务评估，不能把改任务后的指标直接相减。
- [ ] B08-T10 找回评估：真实且授权的检索任务，固定候选集和相关性gold；报告candidate Recall@K与重排nDCG/MRR等各自职责，B09重排不能声称提高未进入候选的召回。同时评估多维过滤是否更容易找回目标收藏。
- [ ] B08-T11 成本/性能runner：记录requested/resolved模型、spec、语言、输入/输出tokens、问题/候选数、p50/p95、attempt/retry、cache命中、review成本。价格使用运行时明确来源与日期，不在代码写未经核验的实时价格。相同硬件/端点/样本条件再比较。
- [ ] B08-T12 Live runner必须显式opt-in、样本上限、最大调用/令牌预算、timeout和失败停止。先展示dry-run清单；无授权不运行。不能从环境有key推断可以收费；普通make verify/CI不得调用模型。请求/响应只写授权私有路径，报告脱敏。
- [ ] B08-T13 晋升规则：先冻结风险/coverage/负担/成本基线与允许变化，再看holdout。P0/数据保护/兼容不变量零容忍，模型质量报告区间和inconclusive；alias漂移/新模型必须重新评估，不把旧校准迁移过去。未达门槛保持shadow/flag off，不能自动晋升。
- [ ] B08-T14 提交工具测试、合成指标golden vectors、报告模板和实际命令；evidence/B08.md区分engineering_done/quality_verified/BLOCKED_EXTERNAL。输出可被B09/B10直接读取的机器报告与人工摘要，记录未完成真实样本数，不能写“已全部通过”掩盖无gold。

## 必须实现的离线指标测试

零预测/零正例/全弃权/合法none；macro与micro明显不同样本；两个Score均值相同但分布不同；group split泄漏检测；错model/spec拒绝混算；重复sample拒绝；阈值重放计数0模型调用；旧单标签与新多标签可比子集报告；固定候选Recall不因重排顺序伪变化；预算恰到上限停止；不含真值的样本不得输出accuracy。

建议新增 `experiments/classification` 或现有实验结构中的独立模块，不挤进生产包。新target命名在实现时确定，需具备validate/evaluate/replay/compare/report/dry-run能力。先写target再引用命令。`make verify`与已有实验保持离线，新增工具对合成fixture输出稳定。

## 报告必须包含

```text
baseline/candidate model + question spec + policy + dataset split SHA
sample counts by language/domain/length/carrier; missing-gold counts
precision/recall/accepted error + coverage + review burden (+ uncertainty)
retrieval candidate recall / reranking metric (separate)
tokens/calls/cost assumptions/p50/p95/cache and retries
ablations with only changed variable and mapping limits
promotion decision with gate config; limitations; live authorization state
```

对应SC06–10/22/25/26/29/30。聚合数据也检查是否可反推出私人收藏；公共报告不包含完整原文、用户note或私人检索词。最终质量结论应能由私有授权数据重新生成，而不是依赖手填Markdown数值。
