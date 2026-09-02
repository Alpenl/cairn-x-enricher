# Go Agent/LLM 框架选型：X 收藏内容增强服务

> 调研日期：2026-09-03（Asia/Shanghai）
> 证据范围：xAI 官方文档、各框架官方文档、官方 GitHub 仓库/Release，以及 Go 官方模块页。
> 安全说明：本文没有调用目标端点，也不包含或复述任何真实凭据。

## 决策

**推荐 CloudWeGo Eino，但生产实现应采用“Eino 框架 + 一个很薄的 xAI Responses 自定义模型适配器”，不能使用经典 OpenAI `ChatModel`，也不能把 Eino 现成 `agenticopenai` 对 `x_search` 的原始 JSON 注入当成正式支持。**

原因是本项目已经有比“OpenAI compatible”更严格的协议事实：

1. 项目侧实测只有 `POST /responses`、模型 `grok-4.6`、`tools: [{"type":"x_search"}]` 能准确读取指定 X 帖子；普通 `/chat/completions` 会产生幻觉。这个实测结果是本项目的本地契约证据，不是框架官方声明。
2. xAI 官方文档确认 X Search 在 OpenAI Responses API 中的工具名就是 `x_search`，由服务端执行；其返回的工具输出项类型是 `x_search_call`。[xAI X Search](https://docs.x.ai/developers/tools/x-search) [xAI Tool Usage Details](https://docs.x.ai/developers/tools/tool-usage-details)
3. xAI 官方文档也确认 Grok 4 系列可把服务端搜索工具与 Responses `text.format` JSON Schema 同时使用。[xAI Structured Outputs](https://docs.x.ai/developers/model-capabilities/text/structured-outputs)
4. Eino 和 Google ADK Go 都能发 Responses 与 `text.format`，但它们当前的官方 Go 转换层都不认识 `x_search_call`。请求可能靠“原始字段注入”发出去，响应却会在框架转换阶段失败。因此需要控制这层很小的 provider-specific 编解码。

这仍然是诚实地使用知名 Go Agent 框架：业务与编排层依赖 Eino 的 `model.AgenticModel` 和 `schema.AgenticMessage`；自定义代码只负责 xAI 特有的 wire protocol，不伪装成 Eino function tool，也不重新实现 Agent runtime。Eino 的正式接口本来就允许第三方实现 `Generate`/`Stream`。[Eino `AgenticModel` 接口](https://github.com/cloudwego/eino/blob/v0.9.19/components/model/interface.go#L97-L101)

## 协议硬门槛

| 能力 | 本项目要求 | 为什么是硬门槛 |
| --- | --- | --- |
| API | `POST /responses` | 已实测 Chat Completions 无法可靠读取目标 X 内容 |
| 模型 | `grok-4.6`，运行时可配置 | 已验证的模型契约；不能由框架默认值替代 |
| 服务端工具 | `tools: [{"type":"x_search"}]` | 需要让 Grok 在 xAI 服务端读取 X 帖与回复，不是让本地程序执行同名函数 |
| 结构化输出 | `text.format.type = "json_schema"` | 结果需要稳定写入数据库字段 |
| 响应解析 | 接受 `x_search_call`，提取最终 `message/output_text` | xAI 官方说明 Responses 输出会包含这种服务端工具调用项 |
| 自定义端点 | `BaseURL` 与 API key 均由运行时配置注入 | 目标是代理端点，不是 OpenAI 默认地址 |

“支持 OpenAI-compatible Base URL”本身不够。若一个适配器只会 `/chat/completions`，或发送 `x_search` 后不能解析 `x_search_call`，它对本项目就是不兼容。

## 候选对比

| 候选 | `/responses` | 自定义 Base URL | Responses `text.format` | 原生 `x_search` 请求/响应 | 维护状态（截至调研日） | 结论 |
| --- | --- | --- | --- | --- | --- | --- |
| **Eino + 自定义 `AgenticModel` 适配器** | 由适配器精确实现 | 是 | 由适配器精确实现 | **是，由项目适配器显式实现** | Eino core `v0.9.19` 于 2026-09-01 发布 | **推荐** |
| Eino `agenticopenai` `v0.2.2` | 是，正式组件 | `ResponsesConfig.BaseURL` | 是，`ResponsesConfig.Text` | 请求仅可用 `ExtraFields` 原始注入；响应转换不支持 | 组件于 2026-06-11 发布，依赖 Eino `v0.9.5` | 仅做短期契约试验，不作生产路径 |
| Google ADK Go `openaimodel` | 是 | `ClientConfig.BaseURL` | **是，一等映射** | 请求仅可用 client option 原始注入；响应转换不支持 | `v2.3.0` 于 2026-08-31 发布；该 package 标记 experimental | 不选现成 adapter；自定义 ADK model 可行但没有胜过 Eino |
| Genkit Go `compat_oai` | 否，走 Chat Completions | 是 | 仅 Chat Completions 结构化输出 | 否 | Go `v1.12.0` 于 2026-08-17 发布 | 协议不匹配，淘汰 |
| langchaingo OpenAI provider | 否，走 Chat Completions | 是 | 仅 Chat Completions response format | 否 | `v0.1.14` 于 2025-10-20 发布；主分支最后提交 2026-01-11 | 协议与维护节奏均不合适 |

## 为什么选择 Eino

### 框架本身成熟且仍活跃

Eino 是 Go-first 的 LLM/Agent 框架，核心抽象包含模型、工具、图编排、Agent Development Kit 和回调观测；它不是一个只包 HTTP 的薄 SDK。[Eino 官方仓库](https://github.com/cloudwego/eino)

- 最新稳定核心为 [`v0.9.19`](https://github.com/cloudwego/eino/releases/tag/v0.9.19)，发布于 2026-09-01。
- 更新的预发布为 [`v0.10.0-alpha.30`](https://github.com/cloudwego/eino/releases/tag/v0.10.0-alpha.30)，发布于 2026-09-02。
- `AgenticModel` 是 `BaseModel[*schema.AgenticMessage]`，只要求实现阻塞 `Generate` 与流式 `Stream`，适合承载 provider-specific adapter。[官方接口源码](https://github.com/cloudwego/eino/blob/v0.9.19/components/model/interface.go#L23-L35)

本服务不需要 ReAct 循环。定时调度、数据库抢占、幂等、重试和事务继续由普通 Go service/repository 完成；Eino 用在模型边界、消息模型、回调与未来编排扩展上。

### Eino 确实已有正式 Responses 组件

经典 `github.com/cloudwego/eino-ext/components/model/openai` 只走 Chat Completions，不适用于已经验证过的目标协议。新的 `github.com/cloudwego/eino-ext/components/model/agenticopenai` 则正式提供 `NewResponsesModel`；`v0.2.2` 已于 2026-06-11 发布。[官方包页](https://pkg.go.dev/github.com/cloudwego/eino-ext/components/model/agenticopenai@v0.2.2) [官方 README](https://github.com/cloudwego/eino-ext/blob/components/model/agenticopenai/v0.2.2/components/model/agenticopenai/README.md)

已核实的能力：

- `ResponsesConfig` 暴露 `APIKey`、`BaseURL`、`Model` 和自定义 HTTP client，底层创建 Responses service。[配置源码](https://github.com/cloudwego/eino-ext/blob/components/model/agenticopenai/v0.2.2/components/model/agenticopenai/responses_model.go#L37-L63)
- `ResponsesConfig.Text` 的类型就是 openai-go 的 `responses.ResponseTextConfigParam`，并被直接写入 `ResponseNewParams.Text`；因此 Responses `text.format` JSON Schema 是一等支持。[配置字段](https://github.com/cloudwego/eino-ext/blob/components/model/agenticopenai/v0.2.2/components/model/agenticopenai/responses_model.go#L80-L90) [请求映射](https://github.com/cloudwego/eino-ext/blob/components/model/agenticopenai/v0.2.2/components/model/agenticopenai/responses_model.go#L541-L583)
- `ExtraFields` 会通过 `option.WithJSONSet` 合并到顶层请求，并覆盖同名字段。因此从纯“发包”角度，可以注入 `tools: [{"type":"x_search"}]`。[原始字段配置](https://github.com/cloudwego/eino-ext/blob/components/model/agenticopenai/v0.2.2/components/model/agenticopenai/responses_model.go#L121-L145) [注入实现](https://github.com/cloudwego/eino-ext/blob/components/model/agenticopenai/v0.2.2/components/model/agenticopenai/responses_model.go#L503-L538)

### 但 Eino 没有正式支持 `x_search`

正式 typed server-tool 配置只有 `WebSearch`、`FileSearch`、`CodeInterpreter`、`Shell`；常量也只列出 `web_search`、`file_search`、`code_interpreter`、`image_generation`、`shell`，没有 `x_search`。[server-tool 配置](https://github.com/cloudwego/eino-ext/blob/components/model/agenticopenai/v0.2.2/components/model/agenticopenai/responses_model.go#L146-L151) [工具常量](https://github.com/cloudwego/eino-ext/blob/components/model/agenticopenai/v0.2.2/components/model/agenticopenai/consts.go#L23-L30)

更关键的是阻塞响应转换器按 openai-go 的已知 union 类型逐一匹配，最后对未知 output item 返回 `unknown output item type`。它没有 `x_search_call` 分支。[响应转换器](https://github.com/cloudwego/eino-ext/blob/components/model/agenticopenai/v0.2.2/components/model/agenticopenai/responses_convertor.go#L1384-L1476)

而 xAI 官方文档明确说 X Search 会在 `response.output[]` 中产生 `x_search_call`。[xAI 输出类型表](https://docs.x.ai/developers/tools/tool-usage-details#using-responses-api)

所以结论是：

- `ExtraFields` 能证明请求大概率发得出去；
- 它不能证明完整调用能成功返回 Eino message；
- 即使某个流式路径碰巧忽略未知事件，也不等于正式支持，升级后行为也没有契约保证；
- 生产代码不应依赖这个偶然行为。

可以保留一个短期 spike：对 `agenticopenai.NewResponsesModel` 设置 `Text` 与 `ExtraFields["tools"]`，用 mock server 和真实端点契约测试验证当前版本行为。即使 spike 成功，也应把它视为可替换优化，而不是架构前提。

## Google ADK Go 核验

Google ADK Go 是强候选，因为它的 OpenAI model 原生走 Responses API，而且维护非常活跃。但它没有解决本项目最特殊的 `x_search_call`。

### 它能正确发送 Responses 与 `text.format`

- `ClientConfig` 提供 `APIKey`、`BaseURL`、`HTTPClient` 和高级 `Options`；底层明确调用 `client.Responses.New`。[官方 `openai.go`](https://github.com/google/adk-go/blob/v2.3.0/model/openaimodel/openai.go#L30-L87)
- 当 `GenerateContentConfig.ResponseSchema` 或 `ResponseJsonSchema` 存在时，请求构造器创建 `ResponseTextConfigParam`，把严格 JSON Schema 放进 `params.Text.Format.OfJSONSchema`。[官方 `request.go`](https://github.com/google/adk-go/blob/v2.3.0/model/openaimodel/request.go#L340-L419)

因此，对问题“ADK Go 的 `openaimodel` 能不能传 Responses `text.format` JSON Schema”，答案是：**能，而且是正式的 typed 映射，不需要逃生口。**

### 它不能正确承载 provider-native `x_search`

- ADK 的工具转换器注释和实现都明确限制为 function tools；非 function tool 会报错。[官方 `tools.go`](https://github.com/google/adk-go/blob/v2.3.0/model/openaimodel/tools.go#L25-L59)
- `ClientConfig.Options` 是高级 openai-go request-option 逃生口。结合 openai-go 官方的 `option.WithJSONSet`，理论上可以在序列化后原始注入 `tools`。[ADK escape hatch](https://github.com/google/adk-go/blob/v2.3.0/model/openaimodel/openai.go#L30-L39) [openai-go 未文档化字段说明](https://github.com/openai/openai-go#undocumented-request-params)
- 但 ADK 的响应转换器只接受 `message`、`function_call` 与 `reasoning`，其他 output item 立即返回 `ErrUnsupportedOutputItemType`；没有 `x_search_call` 或 `web_search_call` 分支。[官方 `response.go`](https://github.com/google/adk-go/blob/v2.3.0/model/openaimodel/response.go#L55-L105)

所以原始注入仍然不是端到端支持。即便最终 `message/output_text` 是正确的，只要同一响应数组中还有 `x_search_call`，ADK 转换就会先失败。

此外，官方 package 注释仍把 `openaimodel` 标为 experimental，可更改或删除。[官方 package 注释](https://github.com/google/adk-go/blob/v2.3.0/model/openaimodel/doc.go#L13-L20) 最新正式版本为 [`v2.3.0`](https://github.com/google/adk-go/releases/tag/v2.3.0)，发布于 2026-08-31。

如果团队本来已经重度采用 ADK，编写自定义 `model.LLM` 同样可行；但这是一个新建的单用途后台服务，ADK 的 session/runner/multi-agent 能力没有带来足以抵消 experimental adapter 和更宽抽象面的收益，因此仍选 Eino。

## 其他候选为何淘汰

### Genkit Go

Genkit Go 本身成熟：Go SDK 已被官方标为 production-ready，`GenerateData[T]` 的类型化输出也很好；最新 Go release [`go/v1.12.0`](https://github.com/genkit-ai/genkit/releases/tag/go/v1.12.0) 发布于 2026-08-17。[Genkit 官方仓库](https://github.com/genkit-ai/genkit)

但官方 `compat_oai` 实现构造的是 `openai.ChatCompletionNewParams` 并调用 Chat Completions，而不是 Responses。[官方 `compat_oai` 源码](https://github.com/genkit-ai/genkit/blob/main/go/plugins/compat_oai/generate.go)

目标代理的 Chat Completions 已实测会幻觉，故这不是可以靠 prompt 或 parser 修补的问题。除非为 Genkit 另写 Responses provider，否则不应选它；既然都要写 adapter，Eino 的 Go-first `AgenticModel` 与服务端工具消息模型更贴合。

### langchaingo

langchaingo 的 OpenAI provider 支持自定义 token、Base URL、JSON Schema 和 function tools，但走的是 Chat Completions。[官方 provider](https://github.com/tmc/langchaingo/tree/v0.1.14/llms/openai) [配置选项](https://github.com/tmc/langchaingo/blob/v0.1.14/llms/openai/openaillm_option.go)

它的最新 release [`v0.1.14`](https://github.com/tmc/langchaingo/releases/tag/v0.1.14) 发布于 2025-10-20，默认分支最后提交为 [`8fea3de`](https://github.com/tmc/langchaingo/commit/8fea3de63675b901cf7a2cdc435c204cb7a93643)，时间为 2026-01-11。协议不匹配之外，维护节奏也明显弱于 Eino、ADK Go 与 Genkit Go。

## 推荐实现边界

```text
定时器
  -> repository 原子领取待处理记录
  -> enrichment service 构造 X 帖 URL/上下文
  -> Eino model.AgenticModel
       -> 自定义 xAI Responses adapter
            -> POST /responses
            -> tools: [{type: x_search}]
            -> text.format: strict JSON Schema
            -> 识别 x_search_call，提取 message/output_text
  -> 业务 JSON 校验与 URL 策略
  -> repository 事务写回成功/失败状态
```

自定义 adapter 只做以下工作：

1. 从运行时配置接收 Base URL、API key、model、timeout 和 retry policy；不读取业务数据库。
2. 生成固定的 Responses 请求体，显式包含 `input`、`tools` 和 `text.format`。
3. 使用标准库 `net/http` 发送请求，或使用官方 openai-go 的 `option.WithJSONSet` 发送 provider 扩展字段。若使用 openai-go，也建议把响应先解到项目自己的最小 wire struct，避免其 typed union 对未知 xAI 类型的约束。[openai-go 未文档化请求/响应字段机制](https://github.com/openai/openai-go#undocumented-request-params)
4. 对 `response.output[]` 做白名单解析：允许并记录 `x_search_call`、允许 `reasoning`，要求恰好存在可用的 `message`/`output_text`；不要无条件忽略所有未知类型。
5. 检查响应状态、拒绝空文本或 refusal，解码 JSON，并映射成 Eino `schema.AgenticMessage`；保留 request ID、token usage 和耗时供 callback/metrics 使用。
6. 实现 Eino `model.AgenticModel`。当前 worker 只调用 `Generate`；`Stream` 可以实现同协议 SSE，或在没有调用方时明确返回“不支持流式”的 typed error，并由测试锁定。

不要把 `x_search` 声明成 Eino function tool。function tool 意味着模型把调用返回给本地程序执行，而 xAI 的 `x_search` 是服务端 built-in tool，两者语义与响应结构都不同。[xAI Tools Overview](https://docs.x.ai/developers/tools/overview)

## 最小请求形状

实际字段以契约测试通过的代理实现为准，模型调用至少应保持下面的 wire shape：

```json
{
  "model": "grok-4.6",
  "input": [
    {
      "role": "user",
      "content": "<X post URL and task>"
    }
  ],
  "tools": [
    { "type": "x_search" }
  ],
  "text": {
    "format": {
      "type": "json_schema",
      "name": "x_enrichment",
      "strict": true,
      "schema": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "original_text": { "type": "string" },
          "summary": { "type": "string" },
          "related_links": {
            "type": "array",
            "items": { "type": "string", "format": "uri" }
          }
        },
        "required": ["original_text", "summary", "related_links"]
      }
    }
  }
}
```

xAI 官方的 Responses 示例展示了 `text.format`、strict JSON Schema 和服务端工具可以组合使用。[官方结构化输出示例](https://docs.x.ai/developers/model-capabilities/text/structured-outputs#structured-outputs-with-tools)

建议提示词保持为一句：

```text
读取指定 X 帖及回复，返回原文、简短摘要和仅与内容直接相关的链接；忽略无关、推广及跟踪链接。
```

结构由 JSON Schema 约束，不要在提示词里重复字段定义。若数据库已经可靠保存原文，工程上更稳的做法是由应用直接复制原文，只让模型返回 `summary` 与 `related_links`，以免模型改写原文；这属于数据源设计决策，不改变框架选型。

## 链接处理注意事项

xAI 的 `citations` 是搜索过程中遇到的全部来源，官方明确说明其中并非每个 URL 都一定被最终回答直接引用。[xAI Citations](https://docs.x.ai/developers/tools/citations)

因此不能把所有 citations 直接写入 `related_links`。应采用两层约束：

- 模型在结构化输出中只选择与帖子内容直接相关的链接；
- 应用再执行 URL 解析、`http`/`https` scheme 白名单、去重、跟踪参数清理、数量上限和域名策略；
- 对从数据库正文/评论中已经提取出的候选 URL，可强制 `related_links` 必须是候选集合的子集；
- X Search 为补全帖子内容产生的检索型引用应单独保存为 provenance，不能自动混入文章相关链接。

## 必须有的契约测试

框架单元测试不能替代代理端点契约测试。CI 或受控集成环境至少覆盖：

1. 请求路径准确为 `/responses`，Base URL 不重复拼接 `/v1`。
2. 请求体模型为配置值，并精确包含 `tools: [{"type":"x_search"}]`。
3. 请求体使用 Responses `text.format`，而不是 Chat Completions `response_format`。
4. fixture 响应先包含 `x_search_call`、后包含 `message/output_text` 时，adapter 能成功提取最终 JSON。
5. `x_search_call` 缺字段、输出只有 tool call、没有 message、多个 message、refusal、无效 JSON、schema 不符时均明确失败。
6. 429、408、5xx、超时和连接错误只做有上限且带抖动的重试；4xx schema/认证错误不盲目重试。
7. 日志不包含 Authorization、完整 prompt、完整响应或敏感 URL query。
8. 一个受 secret 保护、默认不在 fork PR 执行的真实端点 smoke test，验证指定 X 帖可以被准确读取并返回结构化结果。

其中第 4 条是防止未来误换回 Eino/ADK 现成转换器后出现回归的关键测试。

## 版本与安全建议

- 固定 Eino core 的明确版本，不跟随 `latest`；升级通过 Dependabot/Renovate PR 和契约测试完成。
- 若试验 `agenticopenai`，固定 `v0.2.2`。它的 `go.mod` 要求 Go 1.22，依赖 Eino `v0.9.5` 与 openai-go `v3.35.0`；不要假设和任意 core 版本组合都兼容。[组件 `go.mod`](https://github.com/cloudwego/eino-ext/blob/components/model/agenticopenai/v0.2.2/components/model/agenticopenai/go.mod)
- 仓库只提交 `.env.example`；真实 `.env` 必须被 `.gitignore` 排除。GitHub Actions 使用 repository/environment secret，Docker 构建不得把 key 放进 `ARG`、镜像层或 Release asset。
- 对话中曾出现过的 key 应视为已经暴露并立即轮换；本文和代码都不应复述该值。
- `GROK_MODELS_BASE_URL`、`XAI_API_KEY`、`GROK_MODEL` 均应在进程启动时校验，缺失即失败。

## 最终清单

- [x] 框架：CloudWeGo Eino。
- [x] 协议：仅使用 OpenAI Responses API。
- [x] 模型：默认契约为 `grok-4.6`，运行时配置。
- [x] 工具：xAI 服务端原生 `x_search`，不伪装成本地 function tool。
- [x] 输出：Responses `text.format` strict JSON Schema。
- [x] 集成：自定义窄 `model.AgenticModel` adapter，显式解析 `x_search_call`。
- [x] Eino `agenticopenai`：确认有正式 Responses 支持与 JSON Schema 支持，但没有正式 `x_search` 支持。
- [x] Google ADK Go：确认能发 `text.format`，但现成 `openaimodel` 不能端到端处理 `x_search`。
- [ ] 实现后用 mock fixture 与受控真实端点 smoke test 锁定协议。
- [ ] 轮换已经暴露的凭据，并只通过运行时 secret 注入。
