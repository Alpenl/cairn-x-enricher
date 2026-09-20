# B08 / 评估、消融与校准：设计规格

任务进度：[E #14](https://github.com/Alpenl/cairn-x-enricher/issues/14)，B08-T01–T14全文在Issue。B08-T01基线/数据授权准备在B01前开始；工具接B03/B04，检索接B05。UI未完成不是等待理由，gold或live授权缺失只阻塞相应质量验证。

## 数据与真值

固定两仓baseline SHA、v1 schema/policy和已经存在的授权raw；缺失历史标missing，不能新跑后冒充旧输出。数据schema含sample/source revision/hash、语言/载体/长度/完整性、thread/近重复group、多维gold与可接受集合、ambiguity/unknown/not_applicable、标注provenance、检索相关性。

初始200–300条只是建议，按中英混合、短长文、纯外链/图依赖、多主题/引用反驳/词表外/边界分层，显式ID授权导出。困难样本独立复核与争议记录，缺第二标注者说明。模型不是人工gold，旧隐式reviewed也不是；不从原帖给用户立场标真值。

先按thread/近重复分组再冻结train/dev/holdout及seed，支持时间切分。记录holdout访问，不看完再调参；相同snapshot不重复、同查询候选固定。私人数据不进公开Git，聚合也检查泄露风险。

## 指标和消融

离线每label/维度precision/recall、micro/macro、accepted错误率、coverage/弃权、review字段/条、confusion、样本数、零分母与区间。多标签不套单类accuracy，不能无限弃权刷precision。概率校准Brier/ECE/reliability有适用限制与分箱，Score有序误差和分布，confidence/margin价值需要证据，稀有类共享/分组阈值。

阈值仅train/dev拟合，导出risk/损失权重、适用样本、model/spec绑定的policy；同raw重放，问题/state变化需新推断。消融分别v1、局部弃权、词条边界、多维单多选、必要Score、中英文规格、来源角色/备注隔离；任务不同用明确mapping或共同子集，不乱减指标。

真实找回任务固定候选与gold，candidate Recall@K和rerank nDCG/MRR分开，重排不增加漏掉候选的召回。成本性能记录model/spec/语言/tokens/问题候选数/latency/attempt/retry/cache及人工负担；价格有来源日期，同条件才比较。

## Live与晋升

runner有显式opt-in、dry-run清单、样本/调用/token上限、timeout和失败停止；有key不等于授权，普通CI零付费。质量门槛先冻结后看holdout；P0/数据/兼容不变量零容忍，统计不足inconclusive，模型漂移重新评估，未通过shadow/off不自动晋升。

测试零预测/零正例/全弃权/合法none、macro/micro、同均值不同分布、group泄漏、错model/spec、重复sample、重放0调用、v1/v2共同任务、candidate recall不随重排假变、预算到界停止、无gold不生成accuracy。

R07/R10–R12/R17/R21/R23/R29/R34–R36/R39；SC06–10/22/25/26/29/30。交可重复工具、golden vectors、机器报告、evidence/B08.md；报告工程与质量分开，缺gold项仍阻塞，不能以全部代码mock成功称质量通过。任务状态在Issue，报告只是固定版本证据。
