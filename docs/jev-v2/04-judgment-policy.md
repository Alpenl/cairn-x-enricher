# B04 / Jev 判断层与纯策略：设计规格

任务与实时状态：[E #12](https://github.com/Alpenl/cairn-x-enricher/issues/12)，B04-T01–T15完整任务在Issue。接入依赖B02和B03冻结合同；B05词表之前用fixture/旧适配开发。总控[E #10](https://github.com/Alpenl/cairn-x-enricher/issues/10)。

## 三个接口

```text
Evaluate(ctx, evidence, spec) -> RawJudgments  # 可联网
Decide(raw, policy) -> Proposals              # 纯计算
Resolve(proposals, overrides) -> EffectiveView # 纯计算
```

先冻结v1输出fixture，再拆typed transport、questions/compiler、evidence、policy、resolver/replay。旧prompt/schema移明确legacy边界，实验保留且不入生产。官方API/State/三原语/Confidence/限额在实施时核实，记日期及脱敏fixture，不猜在线模型或Score字段。

Noul/Choice/Score采用可区分类型；结构化instructions/criteria与Score有序数组支持。严格验证答案集合、type、概率范围与和、候选/等级、字段、重复键/尾随JSON/大小，保存完整分布。问题ID不提供语义，完整含义在instructions/criteria，独立问题不能依赖未返回的其他答案。

## 语义与证据

词条包含定义、正反例、排除及边界。互斥才Choice；主题/功能/潜在用途可独立Noul；只有产品需要才加有具体等级的Score。客观state物理排除note/why/status/stance，来源角色明确、legacy未知不编造。

预算同时计算state、问题和候选，用已验证tokenizer或注明保守估算，不能把字符当精确token。分块/分批有确定性规则与coverage/truncation，不以未发送材料作判断。相同state独立问题批量，依赖新材料才多调用。

字段决定accepted/rejected/abstained，合法none/empty正常完成；原因有观察/专门判断依据，边缘候选不污染整条。confidence不是独立证据，不默认p乘confidence；Score同均值不同分布保留。旧0.65/0.15 margin冗余有边界测试；是否成立不同于重要性，无深度判断时稳定显式排序。

标签安全上限与展示3个分开，第四有效topic不丢。初始阈值标uncalibrated。用户意图人工或独立opt-in建议，模型不写why/status/stance。job绑定不可变spec、model和generation，alias漂移不能沿用旧校准自动晋升。

## 重放、缓存与运行

replay禁网Decide/Resolve并输出decision/diff，CLI分新分类、策略重放、刷新source，root/help零调用，dry-run不默认写生产。输入不可恢复就明确失败。display/阈值复用raw；语义定义改变重评依赖闭包。缓存包含真实state/criteria/model/batch，先保守整批正确，再验证部分问题重用；缺coverage/跨model不能伪装完整。

有界并发、每job deadline和全局预算，typed错误、组件暂停、可恢复提交、无多层重试。场景包括两个强一弱局部弃权、全低合法空、多功能载体并存、四主题、Score分布、0调用重放、同ID换定义失效、个人字段隔离、引用角色、畸形answer和超长输入。

SC06–10/13/17–18/25–27/29–30；R02/R07–R21/R25/R37。表驱动/fuzz/property、共享fixture、禁网测试、make verify/test-ablation，交evidence/B04.md和companion SHA。可拆多个真实PR，回滚policy使用旧run而非再收费；问题回滚显式切target并保审计。
