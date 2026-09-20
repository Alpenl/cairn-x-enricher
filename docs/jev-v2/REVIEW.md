# Jev v2：审查发现与实施映射

本文件是Issue流程下的审查入口，不是功能实现或质量报告。**原始完整分析已经逐字节保存在[历史审查](archive/REVIEW-20260920.md)**，blob `28597ba8e4335747fd3b207c08a4b3798b05bc57`；原始论证、源码依据与取舍均保留。本文件保留R01–R39稳定编号，去除旧计划PR执行指令，映射至当前Issues。

基线E `cb13d0e543e54f18baf6e151630d728b3175e792`、S `5cf3b0d45c8fbae07d0fb5be1772e7d747c69b4b`。这是当时源码审查，执行时先核实实际版本。前次聊天中的SQLite复现未附可审查产物，仍需真实Worker回归；一条live合成样本只证明接口，不证明真实分类质量。

## 现有基础

### R01 保留source-first和队列边界

原文先存、阅读增强与分类分离、独立lease/revision及人工优先已经存在，不推倒重来。B01/B03/B04/B10持续回归。[stages.go](https://github.com/Alpenl/cairn-x-enricher/blob/cb13d0e543e54f18baf6e151630d728b3175e792/internal/processor/stages.go)。

### R02 不要把已有能力当新成果

多问题同请求、typed answers/model/usage保存已实现。短板是语义结果压回三个topic/单form/单use/global uncertainty；下一步是判断、决策与人工整理分层，而不是再包API。B04。[jev.go](https://github.com/Alpenl/cairn-x-enricher/blob/cb13d0e543e54f18baf6e151630d728b3175e792/internal/classify/jev.go)。

## 标签与不确定性

### R03 内容主题、功能和载体分离

method/tool/data可并存，thread/longform是组织载体，不应单选竞争。primary_form只能是展示投影。B05/B04/B06/B07。[原词表](https://github.com/Alpenl/cairn-share/blob/5cf3b0d45c8fbae07d0fb5be1772e7d747c69b4b/worker/src/taxonomy.json)。

### R04 潜在用途不是个人动机

quote/try/background/material可并存，contra是明确人工针对主张的关系。actionable不等于用户已决定to_try；收藏/引用/作者观点不代表用户立场。B05/B06/B07，个人维度独立。

### R05 同义、层级和相关关系不能混同

llm/AI/agent、eng/前后端等宽窄关系不应都当等价alias，eval可作为跨领域方法。稳定ID保历史含义，显式mapping，不骤增数百标签。B05/B09。

### R06 三主题上限是展示策略

限制散布Go归一化/人工校验、Worker、Web；只改分类器无效。底层完整、有效策略上限独立、卡片显示三项可展开，第四项不自动不确定。B04/B05/B06/B07。[Go taxonomy](https://github.com/Alpenl/cairn-x-enricher/blob/cb13d0e543e54f18baf6e151630d728b3175e792/internal/taxonomy/taxonomy.go)。

### R07 概率不是重要性

不同Noul命题的成立概率不天然可比较主题主次。需要程度时用具体等级，否则显式稳定排序。B04/B09。[Noul](https://docs.typesafe.ai/primitives/noul)。

### R08 全局uncertainty放大审核负担

任一主题0.2<p<0.8就令整条待确认会浪费人工。逐字段接受/拒绝/弃权，让明确值生效；优先复核会改变有效整理/检索的分歧。B04/B06。

### R09 合法空值与服务失败分开

not_applicable、out_of_taxonomy、insufficient_evidence、ambiguous_boundary不等于HTTP失败。原因有观察或专门判断依据，unknown允许，不能从p=0.5臆断缺材料。B03/B04/B05/B06。

### R10 当前margin条件冗余

归一化pmax>=0.65时次高<=0.35，差至少0.30，因此margin0.15不再筛选；微小和误差不改变结论。先边界测试，再离线校准有效规则，不堆阈值。B04/B08。

### R11 confidence不是独立正确率

它概括已有分布，不能默认乘概率，也不证明整体工作流正确。是否有决策增益须评估，稀有标签采用共享/分组策略防过拟合。B04/B08。[Confidence](https://docs.typesafe.ai/confidence)。

## Jev问题与证据

### R12 按语义选Noul、Choice、Score

是否成立用Noul，真正互斥才Choice，程度用具体有序Score。可执行程度从无动作、缺步骤、具体步骤到可运行且可检验；同均值不同分布保留，不给每标签机械加两种问题。B04/B08/B09。[Score](https://docs.typesafe.ai/primitives/score)。

### R13 词条成为可执行定义

补definition/includes/excludes/正反例/边界与概念关系；优先工程vs工具推荐、产品vs设计、评估vs泛体验。结构化instructions/criteria不硬拼长字符串，请求类型支持对象和有序数组。B04/B05。[高级问题](https://docs.typesafe.ai/primitives/advanced)。

### R14 ID不提供语义，问题不共享答案

完整问题含义在instructions/criteria，不能依赖另一个未返回答案。同state独立问题仍批量；真需新材料/候选才第二次调用。B04/B09。[API](https://docs.typesafe.ai/api)、[fan-out](https://docs.typesafe.ai/patterns/fan-out)。

### R15 来源角色与provenance保留

原帖、作者续帖、引用、外链、第三方评论不压成一个context字符串；block ID/URL/关系依据/抓取方式/完整性可追溯，legacy未知不编造。B03/B04/B09。[source.go](https://github.com/Alpenl/cairn-x-enricher/blob/cb13d0e543e54f18baf6e151630d728b3175e792/internal/enrich/source.go)。

### R16 客观与个人输入物理隔离

提示“忽略note”不是隔离；客观HTTP body不含note/why/status，个人建议另行opt-in。同一请求问题共享state。B03/B04。[State](https://docs.typesafe.ai/concepts/state)。

### R17 长度与语言质量实测

实施时核实上下文/问题/候选限额，字节、rune、token估算分清，不静默截断。中文/英文/混合分层评估，中英文问题规格比较；翻译仅辅助有来源，不能替代原文。B04/B08/B09。

## 架构与正确性

### R18 Evaluate/Decide/Resolve分层

HTTP与typed校验、问题编译、policy、人工resolve拆开；后两者纯函数，保留单进程与Go接口。B03/B04/B10。

### R19 失效键分开

evidence、问题语义、actual model、policy各自版本化。label只重显、阈值重放、定义重评依赖、note只个人；同ID不等于可复用。B03/B04/B08。

### R20 当前job.result不足长期审计

该字段会失效/覆盖，需要追加runs和可恢复输入/spec；只有hash不能复现。业务不可变不能阻止用户删除或保留期清理。B03/B10。[迁移0009](https://github.com/Alpenl/cairn-share/blob/5cf3b0d45c8fbae07d0fb5be1772e7d747c69b4b/worker/migrations/0009_independent_classification.sql)。

### R21 固定模型和漂移管理

jev-latest移动而请求字符串不变，校准绑定实际model/spec；记录requested/resolved，漂移先评估，不自动晋升。B01/B04/B08。[Models](https://docs.typesafe.ai/models)。

### R22 P0：消费者交替重跑

同词表A-v2完成、B-v1因不等重领，A再重领且attempt重置是代码允许路径，不代表已发生生产事故。Worker权威target，job绑定spec/generation，旧consumer只声明能力；必须重新生成Worker回归。B01。[classification.ts](https://github.com/Alpenl/cairn-share/blob/5cf3b0d45c8fbae07d0fb5be1772e7d747c69b4b/worker/src/classification.ts)。

### R23 P0：保存原因隐式确认标签

reader.js未reviewed时发送整组classification，Worker人工覆盖并消除uncertainty，既锁错标签又污染反馈。why/status独立、接受明确、历史reviewed不自动当gold。B02/B03/B06/B07/B08。[reader.js](https://github.com/Alpenl/cairn-x-enricher/blob/cb13d0e543e54f18baf6e151630d728b3175e792/internal/dashboard/reader.js)、[curation.ts](https://github.com/Alpenl/cairn-share/blob/5cf3b0d45c8fbae07d0fb5be1772e7d747c69b4b/worker/src/curation.ts)。

### R24 阅读增强不该重抄原文

Transform从旧schema删classification仍要求source再丢弃。独立ReadingResult只生成阅读字段，source由程序注入，保留严格验证。B02/B04/B10。

### R25 错误类型与提交幂等

配置/鉴权/契约、临时限流/网络、stale/superseded分开，有界退避/组件状态/安全日志。已知complete成功但响应丢失不重发模型；未知提供方执行不能虚称全流程exactly-once。B01/B02/B03/B04/B10。

## 产品与扩展

### R26 默认少打扰，依据是真实原文

展示可用值、边缘候选按需接受，分布默认折叠。解释引用真实block/span，有限候选可选none；概率或生成解释不是证据。B06/B07/B09。

### R27 三状态与三种重做分离

来源阅读、AI分类、人工整理分开；只分类retry、纯policy replay、刷新来源分开，不由模型改inbox/kept/compiled/drop。B05/B06/B07。

### R28 字段/tag级人工覆盖

unset、明确空、accept/reject、reset语义独立；重放不复活拒绝，来源变保历史并标适用变化；CAS和幂等保护并发。B03/B05/B06/B07。

### R29 纠正是反馈，不是偏好画像

curation event关联revision/run/spec和明确动作。只存why/浏览不是接受标签，拒绝一条工程不等于不喜欢工程；先用于回归/边界/校准。B03/B06/B08。

### R30 实体独立生命周期

新版entities=[]不应将未运行提取当无实体。候选提取→Jev有限验证/对齐→有来源结果，surface/canonical分开；not_run/failed/empty/stale分开，人工优先，旧结果不假新鲜。B03/B05/B06/B07/B09。

### R31 明确缺口才补证据

图文缺失、外链未取得、截断不同于标签边界。按预算/去重/安全URL补材料、有provenance、不覆盖原文；无授权/能力明确blocked，低置信不一律升级大模型。B09。

### R32 先召回后重排

已过滤有界候选上同rubric比较，稳定分页、预算/cache/原序降级；不增加向量库前提，不声称重排找回漏掉候选。B05/B08/B09。

### R33 受控词表治理

词表外/混淆/明确人工反馈形成提案，人工审批；未知词或一次输出不自动建label。停用/合并有历史mapping，显示与语义变更分别重显/重评。B05/B09。

## 评估与交付

### R34 代表性人工评估集

建议200–300起步不保证稀有标签覆盖；中英混合、短长文/外链/图依赖、多主题/引用反驳/领域外分层，thread近重复分组隔离；无人工gold只报工程。B08。

### R35 指标包括找回与审核负担

每维precision/recall、accepted错误、coverage、审核字段数、补材料成本/延迟及真实找回指标，样本数/区间透明。不能以更自信或无限弃权称变好。B08/B09。

### R36 逐项消融

冻结v1，分别改变局部弃权、定义、单多选、Score、语言/证据规格。阈值重用raw、问题变化新推断；先冻结门槛再看holdout，任务不同明确mapping。B08。

### R37 跨仓兼容与清理

v2显式opt-in，旧type不偷改，Go严格解码、Worker/Android/cache/export联动；新增迁移不改0009，先兼容后启用，回滚不销毁人工/来源。生产与legacy实验边界清晰。B02/B03/B04/B05/B06/B07/B10。

### R38 安全、预算与隐私

保留分离token/受控R2/服务端密钥与日志边界；输入输出/超时/重试/幂等/组件健康有上限。外链防SSRF/redirect/凭据泄漏；评估不公开私人内容，真实质量未过不开flag。所有批次。

### R39 分阶段实证与独立复审

P0先行、合同/纯策略、多维与前端、评估扩展、集成。39发现→126任务→代码PR/commit→30场景证据，迁移/回滚/旧新实例/浏览器/设备不遗漏。工程、真实质量、审查、合并、部署分开，不以叙述“全部完成”代替。B10总验收，任务进度在Issues。

## 当前执行入口

[ISSUE-INDEX](ISSUE-INDEX.md)与[EXECUTION](EXECUTION.md)是唯一现行工作流。完整原文[历史审查](archive/REVIEW-20260920.md)仅用于核对分析，不执行其旧PR导航。模型API/限额/语言等参考不是永久事实，实施时再核实官方文档，无法核实时明确限制。
