# B04 / Jev 判断层与纯决策策略

状态：待实现。总计划#3；base为B02 #4，跨仓依赖 https://github.com/Alpenl/cairn-share/pull/25 （B03）。先同步上游实现、锁定两仓SHA。覆盖R02/R07–R21/R25/R37。

本批不是“再包装一次API”，而是让原始判断可复用、语义与阈值解耦、字段局部弃权。B05将启用正式v2词表；本批先使用B03冻结的v2合成fixture和旧词表适配开发，不等待B05，避免循环依赖。

## 目标模块与接口

将`internal/classify/jev.go`按职责拆分：typed HTTP adapter、question builder/compiler、evidence preparation、policy、resolver/replay；模块名可与现有风格一致。`Evaluate(ctx,evidence,spec)->RawJudgments`可联网；`Decide(raw,policy)->Proposals`、`Resolve(proposals,overrides)->EffectiveView`必须纯函数。生产processor只负责编排与队列，不自行重新解释标签。

## 任务

- [ ] B04-T01 读取项目TypeSafe skill与实施时官方HTTP/State/Noul/Choice/Score/Confidence文档，记录访问日期、真实模型ID、具体限额、返回结构的脱敏fixture。网页不可达时先做本地接口与mock，明确外部契约未验证；不能猜Score字段或精确token上限。
- [ ] B04-T02 冻结现有v1选择fixture，抽离transport与decision但保持legacy输出兼容。将旧prompt/schema renderer移到明确legacy适配边界；production Transform与Jev不再依赖完整旧生成schema。实验仍可运行。
- [ ] B04-T03 typed primitives：Noul/Choice/Score使用可区分类型，instructions/criteria支持结构对象和有序数组，不用map[string]string约束所有问题。按官方格式严格校验答案集合、类型、概率范围/和、选项集合/score等级、必要字段、未知option/重复键/尾随JSON/过大响应；原始分布和实际模型完整保留。容错不得静默吞新字段语义。
- [ ] B04-T04 问题编译器从版本化TermDefinition生成完整语义，包含includes/excludes/边界例子/相关关系。question ID仅做稳定关联，含义在instructions。Choice只用于真的互斥选项；topics/content_functions/affordances可分别用独立Noul；可执行程度/主题深度只在有产品用途时用Score。
- [ ] B04-T05 证据准备读取B03 blocks，客观state物理排除note/why/status/用户立场。primary、author continuation、quoted、third party、external article明确角色与范围；legacy_unknown不编造。序列化测试直接检查敏感个人字段不存在，而不只是提示词写“忽略”。
- [ ] B04-T06 长度与批次预算：同时检查state、问题、criteria/candidates，采用已核实tokenizer或保守明确估算并留余量；字符数不冒充精确token。超预算按确定性块选择/分批规则保留关键材料及truncation状态，不能截断JSON或把未发送内容当已评估。单次独立问题批量处理，dependent问题需要新state才二次调用。
- [ ] B04-T07 纯policy：逐字段/候选accepted、rejected、abstained；合法none/empty可completed。保留边缘候选但不把无关疑问传染全局。reason code从可观察完整性或专门判断产生，不从中间概率臆测。旧uncertainty只能由v1投影策略导出。
- [ ] B04-T08 删除/重设计v1冗余margin规则并写数学边界测试。confidence只作为分布特征候选，不与p简单相乘。Score保留完整分布，同均值不同分布不可折成相同确信。主题是否成立与重要性分离；未启用深度判断时采用稳定显式排序，不跨Noul比较重要性。
- [ ] B04-T09 将有效标签数安全上限和卡片展示上限解耦；不因第四个强主题标不确定。选择策略独立版本化且初始阈值标uncalibrated。没有gold时沿用明确保守baseline或保持shadow，不凭空声称达到95%等效果。
- [ ] B04-T10 与B03连接run/decision存储、target/spec generation、requested/resolved模型和幂等提交。claim指定不可变规格，处理过程中不读取变化的全局词表覆盖它。服务返回alias解析模型不同，记录漂移并禁止沿用旧校准自动晋升。
- [ ] B04-T11 实现离线replay能力：输入已保存runs/spec/policy，禁止网络执行Decide/Resolve，输出新decision/diff，可选择受控提交。CLI区分classify（新推断）、replay（旧判断重算）、refresh-source（明确新获取）；root/help零调用。dry-run默认不写生产；无输入快照返回不可重放而非编造。
- [ ] B04-T12 实现增量影响分析：display/阈值只重放；定义改变只重评依赖闭包。问题重用key包含实际evidence/state/criteria/model和批次语义，partial结果有coverage；同question ID但定义/候选/上下文变更不可复用。先以保守整批cache正确性为底线，再测试独立问题可重用情况，不能跨model混拼。
- [ ] B04-T13 个人意图只保留明确用户输入或单独opt-in建议请求；不得将客观state引入个人备注。模型不写why/curation_status/stance。固定潜在用途说明可由程序模板构造，明确不代表真实收藏动机。
- [ ] B04-T14 扩展独立分类worker的可配置有界并发、单条deadline和整体预算，默认保守。复用B02 typed errors；stale/superseded不打成语义失败，配置错误暂停组件。每job原始结果提交可恢复，不叠多层重试。
- [ ] B04-T15 新增表驱动、fuzz/property、接口fixture与禁网replay测试，make verify、test-ablation。记录运行前后调用计数/变更diff和evidence/B04.md，向B05/B06输出锁定契约与合成样例。

## 必须测试的策略样例（均为合成，不是准确率证据）

| 样例 | 断言 |
|---|---|
| 两个topic=强匹配，一个边缘 | 两个标签accepted，边缘局部abstained，不要求整条审核 |
| 所有topic低但证据完整 | 空结果合法，是否词表外另有明确依据，不触发HTTP重试 |
| form/functions可并存 | tool+method+data可保存；carrier独立 |
| pmax>=0.65 | v1 margin0.15在归一化分布中无独立筛选力，有边界测试 |
| Score同均值两种分布 | raw答案保持差异，policy解释不混淆 |
| 4个强主题 | 底层保留，展示3个可折叠，v1投影合法 |
| 同evidence换threshold/display | HTTP mock调用数=0，新增decision但不新增model run |
| 同ID换定义/候选/model | cache miss或明确受控重评，禁止混用旧raw |
| note中强行要求打标签 | 客观HTTP body不含该note；原文注入不改变任务 |
| 引用反驳与续帖 | 不把引用当同意，不把第三方内容取代主体 |
| 缺/多/畸形answer | 明确contract error，不静默产生空标签 |
| 超长state/多问题 | 有界分批和覆盖状态，不静默截断或失真 |

对应SC06–10、SC13、SC17–18、SC25–27、SC29–30。API契约mock与真实语言质量分开，后者由B08负责。不能为了“充分用Jev”给每个标签无差别增加两三个问题。

## 回滚与交付

v2 evaluator/policy通过feature flag/shadow模式接入，legacy输出可复算对比。回退policy可从既有run重放，不应再次收费；回退到不同问题/spec则保留已有审计并显式切目标。证据中列出哪些模型/语言/限额通过官方核验，哪些仅用模拟fixture。无live授权时完成工程但不宣称生产模型性能验证。
