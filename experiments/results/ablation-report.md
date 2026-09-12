# 消融实验最终报告 — Cairn X Enricher

本报告由 `experiments/` 下的可复现实验程序生成，全部数据来自对真实模型端点的实际调用。

## 一、输入语料的 URL 身份校验

### 为什么必须先做这一步

消融实验的第一版语料把 post ID `1991910395720925418` 标成 `xai` 的帖子，
但该 ID 实际属于 `@karpathy`（Animals vs Ghosts）。模型忠实返回了 Karpathy 的正文，
却因为 URL 标签错误而被判为“身份错误”。这说明：**未校验的语料会伪造出并不存在的缺陷**。
因此每个 sample URL 在计分前都必须经过下面的解析校验。

### 方法

Resolve each sample URL via x_search and compare the returned author handle and permalink against the claimed author in the URL path.

| case | URL 声称作者 | 工具解析作者 | 解析链接 | 结论 |
| --- | --- | --- | --- | --- |
| karpathy_animals_vs_ghosts | @karpathy | @karpathy | https://x.com/karpathy/status/1991910395720925418 | OK |
| karpathy_tokenizer | @karpathy | @karpathy | https://x.com/karpathy/status/1881276282861322459 | OK |
| openai_announcement | @OpenAI | @OpenAI | https://x.com/OpenAI/status/1879628650310991997 | OK |

**结论：全部 sample URL 均解析到声称的作者，语料可信。**

All verified sample URLs resolve to the claimed author. The earlier apparent mismatch was a mislabelled corpus URL, not a pipeline or tool defect.

## 二、消融实验矩阵

| 字段 | 值 |
| --- | --- |
| model | `grok-4.6` |
| endpoint | `https://tk.alpenl.com/v1` |
| variants | 9 |
| samples/variant | 4 |
| 模型调用总数 | 35 |
| 墙钟耗时 | 1282.0s |

### 汇总

| 变体 | 质量 | Δ质量 | 通过率 | 原文保真 | 字段产出 | tokens | Δtokens | 延迟 | 搜索证据 | 语言错 | 结构失败 | 上游失败 | 跳过 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `FULL` | 0.994 | — | 100% | 0.96 | 1.00 | 12563 | — | 84.9s | 4/4 | 0 | 0 | 0 | 0 |
| `no_thread_context` | 0.994 | +0.000 | 100% | 0.96 | 1.00 | 11103 | -1460 | 64.1s | 4/4 | 0 | 0 | 0 | 0 |
| `no_search_evidence_gate` | 0.994 | — | 100% | 0.96 | 1.00 | 12330 | -233 | 89.1s | 4/4 | 0 | 0 | 0 | 0 |
| `unconstrained_title` | 0.994 | — | 100% | 0.96 | 1.00 | 11642 | -921 | 78.4s | 4/4 | 0 | 0 | 0 | 0 |
| `no_classification` | 0.944 | -0.050 | 100% | 0.96 | 0.88 | 11707 | -856 | 77.9s | 4/4 | 0 | 0 | 0 | 0 |
| `source_only` | 0.900 | -0.094 | 100% | 1.00 | 1.00 | 8778 | -3784 | 83.5s | 0/3 | 0 | 0 | 1 | 0 |
| `no_translation` | 0.894 | -0.100 | 100% | 0.96 | 0.88 | 11839 | -724 | 79.3s | 4/4 | 0 | 0 | 0 | 0 |
| `no_x_search` | 0.000 | -0.994 | 50% | 0.09 | 0.78 | 7438 | -5125 | 70.3s | 0/4 | 0 | 2 | 0 | 0 |
| `no_strict_schema` | 0.000 | -0.994 | 0% | 0.00 | 0.25 | 12928 | +365 | 148.3s | 3/3 | 0 | 3 | 0 | 0 |

> 上游失败与跳过不计入质量均值，避免端点抖动被误读为质量回退。

### 各变体消融的设计决策

| 变体 | 被消融的决策 |
| --- | --- |
| `FULL` | none — the production pipeline |
| `no_thread_context` | thread comment retrieval (post-only prompt) |
| `no_search_evidence_gate` | the requirement that a completed X search be observed |
| `unconstrained_title` | the Chinese/title-length validator |
| `no_classification` | taxonomy classification, why_suggestion and entities |
| `source_only` | x_search replaced by already-stored original text |
| `no_translation` | simplified-Chinese translation field |
| `no_x_search` | server-side x_search tool and tool_choice=required |
| `no_strict_schema` | strict JSON Schema constrained decoding |

### 逐样本明细

#### `FULL`

| sample | 通过 | 质量 | 标题 | 语言 | 原文 | 译文 | 链接 | 图片 | tokens | 延迟 | 备注 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 301 | yes | 1.00 | 动物智能与LLM智能源于不同优化压力 | en | 3128 | 959 | 0 | 0 | 14488 | 111.2s |  |
| 304 | yes | 1.00 | DeepSeek V3竞技场排名第七 | zh | 128 | 128 | 0 | 3 | 12340 | 84.1s |  |
| 303 | yes | 1.00 | OpenAI扩展ChatGPT本地化危机热线 | en | 333 | 188 | 1 | 0 | 11541 | 66.9s |  |
| 302 | yes | 0.98 | Transformer神经算子求解复杂PDE论文 | ja | 160 | 127 | 1 | 1 | 11882 | 77.3s |  |

#### `no_classification`

| sample | 通过 | 质量 | 标题 | 语言 | 原文 | 译文 | 链接 | 图片 | tokens | 延迟 | 备注 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 302 | yes | 0.93 | 机器学习结合Transformer解偏微分方程论文 | ja | 160 | 133 | 1 | 1 | 10924 | 80.2s |  |
| 304 | yes | 0.95 | DeepSeek V3竞技场排名第七性价比高 | zh | 128 | 128 | 0 | 3 | 11272 | 65.4s |  |
| 303 | yes | 0.95 | OpenAI扩展ChatGPT本地化危机热线 | en | 333 | 183 | 1 | 0 | 11051 | 69.1s |  |
| 301 | yes | 0.95 | 卡帕西谈动物智能与大模型智能的根本差异 | en | 3128 | 935 | 0 | 0 | 13582 | 96.8s |  |

#### `no_search_evidence_gate`

| sample | 通过 | 质量 | 标题 | 语言 | 原文 | 译文 | 链接 | 图片 | tokens | 延迟 | 备注 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 301 | yes | 1.00 | 卡帕西解析动物智能与大模型智能的本质区别 | en | 3128 | 956 | 0 | 0 | 13883 | 107.3s |  |
| 303 | yes | 1.00 | OpenAI扩大ChatGPT本地化危机热线 | en | 333 | 184 | 1 | 0 | 11375 | 74.8s |  |
| 304 | yes | 1.00 | DeepSeek V3竞技场排第七性价比高 | zh | 128 | 128 | 0 | 3 | 12647 | 90.8s |  |
| 302 | yes | 0.98 | 结合Transformer与神经算子求解PDE论文 | ja | 160 | 138 | 1 | 1 | 11414 | 83.5s |  |

#### `no_strict_schema`

| sample | 通过 | 质量 | 标题 | 语言 | 原文 | 译文 | 链接 | 图片 | tokens | 延迟 | 备注 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 304 | no | 0.00 |  |  | 0 | 0 | 0 | 0 | 12766 | 95.2s | decode structured output: json: unknown field "title" |
| 303 | no | 0.00 |  |  | 0 | 0 | 0 | 0 | 13025 | 108.1s | decode structured output: json: unknown field "title" |
| 302 | no | 0.00 |  |  | 0 | 0 | 0 | 0 | 12992 | 241.7s | decode structured output: json: unknown field "title" |

#### `no_thread_context`

| sample | 通过 | 质量 | 标题 | 语言 | 原文 | 译文 | 链接 | 图片 | tokens | 延迟 | 备注 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 304 | yes | 1.00 | DeepSeek V3竞技场评分排第七 | zh | 128 | 128 | 0 | 1 | 10541 | 49.0s |  |
| 303 | yes | 1.00 | OpenAI扩展ChatGPT本地化危机热线 | en | 333 | 190 | 1 | 0 | 10271 | 51.6s |  |
| 302 | yes | 0.98 | 机器学习结合Transformer求解偏微分方程 | ja | 160 | 128 | 1 | 1 | 10736 | 69.5s |  |
| 301 | yes | 1.00 | 卡帕西：动物与LLM智能优化压力迥异 | English | 3128 | 897 | 0 | 0 | 12864 | 86.3s |  |

#### `no_translation`

| sample | 通过 | 质量 | 标题 | 语言 | 原文 | 译文 | 链接 | 图片 | tokens | 延迟 | 备注 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 302 | yes | 0.88 | 机器学习解PDE的Transformer神经算子论文 | ja | 160 | 0 | 1 | 1 | 11355 | 90.7s |  |
| 303 | yes | 0.90 | OpenAI扩展ChatGPT危机热线支持 | en | 333 | 0 | 1 | 0 | 10566 | 53.0s |  |
| 304 | yes | 0.90 | DeepSeek V3竞技场排名第七性价比高 | zh | 128 | 0 | 0 | 3 | 12302 | 83.0s |  |
| 301 | yes | 0.90 | 卡帕西：动物智能与LLM智能优化压力不同 | en | 3128 | 0 | 0 | 0 | 13134 | 90.7s |  |

#### `no_x_search`

| sample | 通过 | 质量 | 标题 | 语言 | 原文 | 译文 | 链接 | 图片 | tokens | 延迟 | 备注 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 302 | no | 0.00 | 检查日语技术X帖的原文语言保留情况 | ja | 0 | 0 | 1 | 0 | 6795 | 56.2s | original_text missing |
| 304 | no | 0.00 | 指定X帖记忆中无具体原文 | zh | 0 | 0 | 0 | 0 | 8049 | 84.3s | original_text missing |
| 303 | yes | 0.00 | OpenAI官方公告附帮助文档链接 | en | 136 | 60 | 1 | 0 | 6310 | 52.0s |  |
| 301 | yes | 0.00 | 深入探讨动物智能和大语言模型智能的根本差异 | en | 1251 | 385 | 0 | 0 | 8598 | 88.9s |  |

#### `source_only`

| sample | 通过 | 质量 | 标题 | 语言 | 原文 | 译文 | 链接 | 图片 | tokens | 延迟 | 备注 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 303 | yes | 0.70 | OpenAI扩展ChatGPT本地化危机热线 | en | 330 | 186 | 1 | 0 | 8351 | 78.0s |  |
| 302 | yes | 1.00 | Transformer结合神经算子求解偏微分方程的机器学习论… | ja | 136 | 109 | 0 | 0 | 10723 | 115.9s |  |
| 304 | yes | 1.00 | DeepSeek V3竞技场排第七性价比最高 | zh | 128 | 128 | 0 | 0 | 7261 | 56.6s |  |
| 301 | no | 0.00 |  |  | 0 | 0 | 0 | 0 | 0 | 784.2s | 上游失败（不计分） |

#### `unconstrained_title`

| sample | 通过 | 质量 | 标题 | 语言 | 原文 | 译文 | 链接 | 图片 | tokens | 延迟 | 备注 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 302 | yes | 0.98 | 机器学习求解PDE论文：Transformer与神经算子 | ja | 160 | 132 | 1 | 1 | 11550 | 91.7s |  |
| 301 | yes | 1.00 | Karpathy：动物智能与LLM智能的优化压力本质差异 | en | 3128 | 938 | 0 | 0 | 13688 | 106.9s |  |
| 304 | yes | 1.00 | DeepSeek V3竞技场第7，前十性价比最高 | zh | 128 | 128 | 0 | 3 | 10625 | 56.1s |  |
| 303 | yes | 1.00 | OpenAI扩展ChatGPT本地化危机热线 | en | 333 | 187 | 1 | 0 | 10704 | 58.8s |  |

### 失败模式统计

- no_strict_schema :: decode structured output: json: unknown field "title" (×3)
- no_x_search :: original_text missing (×2)
- source_only :: model HTTP 502: {"error":{"message":"Upstream service temporarily unavailable","… (×1)
