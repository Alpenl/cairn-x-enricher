# 原文存档与 Jev 独立分类

## 生产流程

1. `ResponsesClient.FetchSource` 只获取原文、语言、引用/评论上下文、链接与图片 URL。
   原帖和上下文分开；原文要求已完成的搜索工具证据，并校验链接/图片来源。
2. `POST /api/enrichment/jobs/{id}/source` 在当前有效的获取 lease 下保存快照和原文，
   同一事务将分类任务入队。后续失败不会撤销原文。
3. `ResponsesClient.Transform` 根据存档原文生成中文标题、译文和摘要，不携带词表、
   不执行搜索。模型返回的原文不能覆盖快照。图片继续走受控 R2 存储。
4. Jev 分类队列独立领取任务并绑定原文与上下文证据。语义判断成功后只更新 AI 建议，
   不写原文、阅读增强、人工 `curation`、`why` 或 `curation_status`。

生产 `serve`/`once` 使用 `processor.NewStaged`。旧 `Generate`/`Workflow` 为消融实验和
旧基线保留，不参与生产分类。单个服务仍是一个 Go 进程；不增加外部队列或数据库。

## Jev 契约和规则

服务端调用 `POST https://api.typesafe.ai/v1/systemone`，使用 Bearer key。
生产入口固定 `jev-1.13.0`，并核对响应模型。`Evaluate` 保存 typed answers、分布、
实际请求身份和 usage；`Decide` 与 `Resolve` 为纯函数，可以零模型调用重放。

不可变 spec 来自版本化词表和问题编译器，目标由 Worker 的 spec/model/policy/generation
决定。主题、功能等独立判断与互斥 Choice 分开；标签安全上限与界面展示数量分开，
不会因为默认展示三个而丢弃第四个有效主题。字段分别接纳、拒绝或弃权，合法空结果正常完成。
阈值仍标记为未校准。完整合同见 [B04 判断层](jev-v2/04-judgment-policy.md)。

客观判断使用绑定的来源证据，不读取用户 note/why/status/stance；个人意图来自人工维护。
自动 `contra` 被当前 policy 和 Worker 写入口拒绝，历史读取和明确人工选择保留。
实体为独立、默认关闭的候选与原文 span 能力，不能由分类模型自由编造名称。

## 独立状态和一致性

`classification_jobs` 保存独立状态、attempt、lease、输入 revision、目标 generation、
不可变 spec、内容版本和 evidence snapshot 身份。提交必须仍匹配当前绑定；
过期输入不能覆盖新来源或人工整理。来源快照与阅读增强先后保存，分类失败不撤销原文。
个人字段变化与原文内容版本分离，不因整理备注而重新抓取来源。

瞬时错误沿用服务端退避和最多五次 attempt；stale 属于失效工作，配置/协议/预算错误暂停
组件，不伪装成语义失败。没有在模型 HTTP 层增加多层重试。目标切换必须通过 B01 的
受控 generation，不因本地改环境变量而自动激活。历史收藏不隐式全库入队。

获取/阅读任务保留 `MAX_CONCURRENCY`；分类由独立串行 worker 执行，单条有 deadline。
`once` 的 `--max-jobs` 是单轮任务上限，下面的 D1 日预算跨轮次和进程生效。

普通文本和绑定的结构化快照使用相同的输入预算：默认正文加上下文最多 12,000 个 Unicode 字符、12 个上下文块，序列化后的 state 最多 48 KiB，包含问题 instructions/criteria 的完整 HTTP 请求最多 128 KiB。这些是应用自身的限制，不是供应商 token 数或上下文窗口的估算。优先保留原帖；超限时缩短请求正文或移除末尾上下文块，并记录 `truncated`/`coverage`。数据库完整快照保持不变。问题定义本身导致整包超限时，发送前返回合同错误，不删改问题含义；复用与扩展请求也受完整请求字节上限约束。

## 持久调用预算

普通分类在一套 Worker/D1 部署内按 UTC 自然日共享预算。每次实际 Jev HTTP 请求都先经
内部 `POST /api/v2/classification-budget/reserve` 原子授权；全量、部分重用和拆批经过
同一入口，每个拆批分别计数。与[扩展预算](jev-v2/09-semantic-extensions.md)的 20/2
额度分开；两个池不是 TypeSafe 账户级总额度，也不跨不同 Worker 部署共享。

| 环境变量 | 默认值及服务端上限 |
| --- | ---: |
| `CAIRN_CLASSIFICATION_MAX_CALLS` | 20 |
| `CAIRN_CLASSIFICATION_MAX_CALLS_PER_ITEM` | 5 |
| `CAIRN_CLASSIFICATION_MAX_INPUT_TOKENS` | 1310720 |
| `CAIRN_CLASSIFICATION_MAX_INPUT_TOKENS_PER_ITEM` | 327680 |

四项只允许正整数且只能收紧。每次预留固定 **65536 input tokens**，是按固定 Jev 1.13
请求上限采用的保守额度，不是实际 usage 或 tokenizer 估算；实际 usage 另存。
依据：[当前模型说明](https://docs.typesafe.ai/models)。不足一次预留时不调用模型。
多个客户端的较低设置约束各自申请；服务端上限约束所有消费者。午夜重置，非滑动 24 小时。

授权在同一事务验证有效 lease、输入/内容版本、spec、当前 generation 和证据身份。
重复 operation key 不再次授权，异 payload 冲突；丢失授权响应、后端不可用或非法确认
均不调用模型，不重试授权、不退款。调用失败或取消后的已消费额度也不退回。
匿名全局账本不保存收藏 ID、lease、请求正文或原文；删除收藏清除逐条记录但不退回全局额度。
本次复用已有账本和 0026 删除守卫，无新增迁移。

全局耗尽在领取任务前暂停，不继续增加 attempt；逐条耗尽则跳过该条。
已领取任务完全复用兼容 raw 时不申请新额度，纯策略 replay 也为零调用；全局耗尽时
不会为了尝试复用而额外领取新任务。拆批中途耗尽停止后续请求，保留实际调用记录并标明 partial。

## 配置与使用

`.env` 使用 `TYPESAFE_API_KEY`、`TYPESAFE_BASE_URL=https://api.typesafe.ai`、
`TYPESAFE_MODEL=jev-1.13.0`。API 根地址不含 `/v1`。CLI 自动读取根目录 `.env`，
Docker 通过已有 `env_file` 注入；配置文件被 Git 忽略。
`serve`/`classify` 在启动网络操作前拒绝其他模型或 alias，现有目标须按 B01 受控切换。

```bash
# 只处理分类，不需要 Grok 凭据或调用 Grok。
go run ./cmd/cairn-x-enricher classify --max-jobs 20
# 显式入队一条历史原文；队列也可能含其他待处理条目。
go run ./cmd/cairn-x-enricher classify --id 123 --max-jobs 20
```

单条状态通过内部 `GET /api/enrichment/classifications/{id}` 查看；
`/status` 统计 `classified` 和 `classification_failed`。预算错误按组件暂停处理。

## 升级与回退边界

1. 停止并排空旧分类消费者，避免升级前已经取得的 lease 继续走旧付费路径。
2. 先准备含全部迁移（当前截至 0028）和预算端点的 Worker；检查备份与恢复。
3. 新消费者请求和 Worker 响应均声明 `X-Cairn-Classification-Budget: 1`。
   旧消费者面对新 Worker 不能领取任务；新消费者面对旧 Worker 在握手阶段停止，不能领取任务。
4. 配置固定模型并通过既有受控接口建立匹配的目标 generation，再限定 ID 小批验证。

预算不能追溯约束升级前的旧 lease。回退到无预算消费者不能继续运行付费分类，
应停止分类并保留兼容 Worker/账本。关闭扩展 flag 不会关闭普通分类预算。
本批只提交代码和验收证据，未部署、未迁移生产、未自动激活目标或执行全库回填。

## 验证和效果边界

普通 `make verify` 不调用外部模型。覆盖独立获取/阅读契约、概率分布校验、
原文在阅读失败后保留、分类重试不抓取原文，以及 D1 输入版本与人工结果保护。

可显式执行一条合成样本的真实 API 检查（从本地 `.env` 读取 key，需配套 Worker 词表文件）：

```bash
CAIRN_TEST_LIVE_TYPESAFE=1 go test -run '^TestLiveJev$' -v ./internal/classify
```

2026-09-20 使用完整词表的一次合成样本返回 `jev-1.13.0`，耗时约 0.77 秒，
输入/输出 token 为 4637/430，标签为 `eval` 和 `llm`，形态 `method`、用途 `try`。
其他主题的中间概率触发了保守的待确认标记。这个结果证明 API 契约可用，
不代表真实收藏集的分类准确率或性能基准。按所有者授权，后续使用带来源和生成记录的 `automatic_reference` 样本评估，无需人工标注；
自动参考不能称为 human gold。训练/开发/保留集隔离，未决结果不能升级为质量达标。

项目技能位于 `.agents/skills/typesafe-ai/`。权威 API 参考：
[HTTP API](https://docs.typesafe.ai/api)、[Noul](https://docs.typesafe.ai/primitives/noul)、
[Choice](https://docs.typesafe.ai/primitives/choice)。
