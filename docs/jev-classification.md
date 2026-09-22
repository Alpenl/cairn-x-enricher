# 原文存档与 Jev 独立分类

## 生产流程

1. `ResponsesClient.FetchSource` 只获取原文、语言、引用/评论上下文、链接与图片 URL。
   原帖和上下文分开；原文要求已完成的搜索工具证据，并校验链接/图片来源。
2. `POST /api/enrichment/jobs/{id}/source` 在当前有效的获取 lease 下保存快照和原文，
   同一事务将分类任务入队。后续失败不会撤销原文。
3. `ResponsesClient.Transform` 根据存档原文生成中文标题、译文和摘要，不携带词表、
   不执行搜索。模型返回的原文不能覆盖快照。图片继续走受控 R2 存储。
4. Jev 分类队列独立领取原文、上下文与收藏备注。语义判断成功后只更新 AI 建议，
   不写原文、阅读增强、人工 `curation`、`why` 或 `curation_status`。

生产 `serve`/`once` 使用 `processor.NewStaged`。旧 `Generate`/`Workflow` 为消融实验和
旧基线保留，不参与生产分类。单个服务仍是一个 Go 进程；不增加外部队列或数据库。

## Jev 契约和规则

服务端调用 `POST https://api.typesafe.ai/v1/systemone`，使用 Bearer key，
默认 `jev-latest`，记录 API 实际返回的模型标识。问题一次构造、同一请求并行回答：

- `topics`：每个 active 主题一个 Noul，概率 >= 0.8 接纳，最多取三个；
  0.2 < p < 0.8 标记待确认；超过三个强匹配也标记待确认。
- `form`、`use`：分别使用 Choice，包含 `none`。最高概率 < 0.65 或与次高差 < 0.15
  时留空并标记待确认。选择 `none` 同样留空。
- 这些阈值是初始保守策略，未经真实收藏集校准。`uncertainty` 不是接口调用失败，
  调用成功但待确认的结果按正常分类完成存储。
- `why_suggestion` 使用明确标注的用途模板，不生成用户真实收藏动机。
- 第一版不生成自由实体名称，`entities` 为空；新分类会替换旧 AI 实体建议。
  实体提取如需恢复，应作为独立的候选提取能力实现。
- 保存完整 typed answers、token usage、实际模型与策略版本，便于离线复核。

词表唯一来源仍为 Worker `src/taxonomy.json`，增加可选 `description` 定义，
当前版本 `2026-09-20.1`。问题与阈值集中在 `internal/classify/jev.go`。
更改问题含义或阈值时必须递增 `PolicyVersion`。词表修改应递增 `taxonomy.version`。

## 独立状态和一致性

`classification_jobs` 保存 `pending/processing/completed/failed/exhausted/waiting_source`、
attempt、next retry、独立 lease、输入 revision、词表/策略/请求模型版本及审计结果。
已存档来源继续使用现有 `links` 阅读字段；原始来源快照存于 `enrichment_sources`。

- 模型调用错误按 1m / 5m / 30m / 2h 退避，最多五次；没有在客户端叠加重试。
- 源内容、URL、备注改变会增加 revision、清除租约并失效旧 AI 分类。
  完成提交同时检查 lease token、有效期、revision、词表和策略版本。
- 修改 URL/备注仍沿用当前 App 的原文失效策略。未来可进一步优化为仅备注变更时保留原文。
- 分类策略、词表或请求模型改变后，已入队且有原文的记录会自动重新分类。
  `jev-latest` 指向新模型不会改变请求模型字符串；要强制重跑可改变策略版本或按 ID 入队。
- 活跃租约不会被重跑抢占。历史内容不自动入队，可按 ID 显式加入。
- 分类结果写入同样触发 App 内容缓存失效。没有修改公共列表/详情的字段集合。

获取/阅读任务的并发上限仍为 `MAX_CONCURRENCY`；分类使用额外的一个串行 worker。
两类队列同时运行，互不因批次领取窗口而饥饿；人工获取完成后最迟下个轮询周期分类。
`once` 在预算内最多各处理 `--max-jobs` 条获取任务和分类任务。

普通文本和绑定的结构化快照使用相同的输入预算：默认正文加上下文最多 12,000 个 Unicode 字符、12 个上下文块，序列化后的 state 最多 48 KiB，包含问题 instructions/criteria 的完整 HTTP 请求最多 128 KiB。这些是应用自身的限制，不是供应商 token 数或上下文窗口的估算。优先保留原帖；超限时缩短请求正文或移除末尾上下文块，并记录 `truncated`/`coverage`。数据库完整快照保持不变。问题定义本身导致整包超限时，发送前返回合同错误，不删改问题含义；复用与扩展请求也受完整请求字节上限约束。

## 配置与使用

`.env` 使用 `TYPESAFE_API_KEY`、`TYPESAFE_BASE_URL=https://api.typesafe.ai`、
`TYPESAFE_MODEL=jev-latest`。API 根地址不含 `/v1`。CLI 自动读取根目录 `.env`，
Docker 通过已有 `env_file` 注入。配置文件应被 Git 忽略并设为权限 0600。

```bash
# 只处理分类，完全不触发 Grok。
go run ./cmd/cairn-x-enricher classify --max-jobs 20
# 将一条历史原文入队后消费队列；可能也会消费其他待处理条目。
go run ./cmd/cairn-x-enricher classify --id 123 --max-jobs 20
```

`classify` 复用服务的配置校验，因此仍要求完整的现有环境配置，但不会连接 Grok。
Jev 单条状态和概率通过内部 `GET /api/enrichment/classifications/{id}` 查看；
`/status` 的批次统计增加 `classified` 和 `classification_failed`。
当前网页仍显示原有获取/阅读任务状态，尚无独立分类队列的可视化操作面板。

## 升级

1. 在配套 `cairn-share/worker` 验证并应用迁移 0009，发布新 Worker 和词表。
2. 在 Enricher 环境配置 TypeSafe key，构建/发布新版本，再启动服务。
3. 小批量验证原文存档、阅读增强和分类。不要直接启动全库回填。

本改动不自动部署 Worker、不应用远端迁移、不更新 NAS 镜像。
回滚应用时可保留新增表；旧 Worker 不消费分类队列。旧单次生成路径的分类结果仍兼容。

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
不代表真实收藏集的分类准确率或性能基准。正式质量评估仍需人工标注的代表性样本。

项目技能位于 `.agents/skills/typesafe-ai/`。权威 API 参考：
[HTTP API](https://docs.typesafe.ai/api)、[Noul](https://docs.typesafe.ai/primitives/noul)、
[Choice](https://docs.typesafe.ai/primitives/choice)。
