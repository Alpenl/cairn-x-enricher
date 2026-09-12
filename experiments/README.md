# 消融实验（Ablation Study）

本目录是对 `cairn-x-enricher` 富化流水线的受控消融实验。所有结果都来自对**真实模型端点**
的实际调用，不是模拟或占位数据。

## 为什么需要消融实验

流水线里有很多“看起来显然有用”的设计：`x_search` 工具、`tool_choice=required`、strict JSON
Schema、线程评论读取、简体中文译文、封闭词表分类、搜索证据门禁、中文字数校验器。
每一条都在消耗 token 和延迟。消融实验逐个移除其中一条，用同一批输入测量质量与成本的变化，
从而区分**真正承重的设计**和**装饰性设计**。

## 目录结构

```text
experiments/
├── ablation/            # 实验框架：变体定义、评分、运行器
│   ├── harness.go       # 低层 Responses 协议客户端、重试、基础设施失败区分
│   ├── variants.go      # 9 个消融变体的定义
│   ├── score.go         # 机械化评分（无模型裁判）
│   ├── samples.go       # 冻结语料
│   ├── run.go           # 矩阵执行与聚合
│   └── main/main.go     # CLI 入口
├── harvest/             # 从 Worker 拉取真实已完成书签，生成真实语料
├── threadvalue/         # 线程读取 vs 只读原帖的真实数据对比
├── threadreport/        # 渲染 thread-value 结果
├── verifyurls/          # 语料 URL 身份校验（计分前的强制门禁）
├── rescore/             # 用当前语料/评分器重算已有 journal，无需重新付费调用
├── identity/            # 语料身份解析记录
├── ablation-report/     # 把 JSON 结果渲染成 Markdown
└── results/             # 生成的实验产物
```

## 实验变体

| 变体 | 移除的设计 |
| --- | --- |
| `FULL` | 无（生产配置基线） |
| `no_x_search` | 服务端 `x_search` 工具与 `tool_choice=required` |
| `no_thread_context` | 线程/评论读取（只用 post-only 提示词） |
| `no_strict_schema` | strict JSON Schema 约束解码 |
| `no_translation` | 简体中文译文字段 |
| `no_classification` | 词表分类、`why_suggestion`、`entities` |
| `no_search_evidence_gate` | “必须观察到已完成的 X 搜索”这一验收条件 |
| `unconstrained_title` | 标题的中文与字数校验器 |
| `source_only` | 用已存原文替代 `x_search` 的恢复路径 |

## 评分方法

评分完全机械化，任何人都能用同一份 JSON 复算：

- **接受率** — 复用生产校验规则（标题 8–32 字符且含中文、原文/译文/摘要非空、链接与图片格式合法）。
- **原文保真度** — 与冻结参考文本的字符三元组 Jaccard 相似度。
- **Grounded 门禁** — 语料已知原文时，保真度低于 0.45 判定为“编造”，质量直接为 0。
  这是最重要的评分规则：结构完全合法但内容编造的结果必须被识别出来，
  否则会得出“去掉 `x_search` 既便宜又不掉质量”的相反结论。
- **字段产出率** — 8 个生产字段中可用字段的比例。
- **语言正确性** — 声明的 `original_language` 与正文实际使用的文字是否一致（防止把原文翻译掉）。
- **成本** — token 与延迟。
- **质量分** — 接受(0.40) + 标题合规(0.10) + 译文(0.10) + 摘要(0.05) + 语言(0.05)
  + 保真度×0.15 + 链接(0.05) + 图片(0.05) + 分类(0.05)，伪造风险扣 0.30。

## 关键方法学约束

1. **语料必须先做 URL 身份校验。**
   `verifyurls` 会用端点解析每个 sample URL 的真实作者，并与 URL 路径中声称的作者比对。
   第一版语料曾把 post ID `1991910395720925418` 错标成 `xai` 的帖子（实际属于 `@karpathy`），
   导致一个完全正确的模型响应被判为“身份错误”。**未校验的语料会伪造出不存在的缺陷。**

2. **基础设施失败不计入质量。**
   该端点在并发下会返回 `502/503`。这类运行从不产生模型输出，因此被标记为
   `infra_failed`、从质量均值中剔除，并单独计数。否则端点抖动会被误读为质量回退。

3. **每个变体在同一批样本上运行。**
   变体之间唯一的差异是被消融的那一项设计。

## 真实数据复测

```bash
# 拉取真实收藏（只读，不触发模型调用）
go run ./experiments/harvest -env .env -limit 100 -out experiments/testdata/real-corpus.json

# 逐书签对比“读线程”与“只读原帖”
go run ./experiments/threadvalue -env .env -max 17 -concurrency 3 \
  -out experiments/results/thread-value.json -timeout 120m
go run ./experiments/threadreport experiments/results/thread-value.json
```

## 复现

```bash
# 0. 前置：确认语料 URL 身份正确（失败即中止，不得计分）
go run ./experiments/verifyurls -env .env

# 1. 运行完整矩阵（约 30–60 分钟，取决于端点状态）
go run ./experiments/ablation/main -env .env -concurrency 2 \
  -out experiments/results/ablation.json -timeout 150m

# 2. 渲染报告
go run ./experiments/ablation-report experiments/results

# 3. 语料或评分规则修正后，无需重新调用模型即可重算
go run ./experiments/rescore -in experiments/results/ablation.jsonl \
  -out experiments/results/ablation.json
```

`-journal` 控制增量日志路径（默认与 `-out` 同名 `.jsonl`）。程序会**自动续跑**：
日志中已有非基础设施失败的 (变体, 样本) 会被直接沿用，因此长时间实验可安全中断重跑。

`-variants` 与 `-samples` 可传入逗号分隔的列表做子集运行，例如
`-variants FULL,no_strict_schema -samples 201,202`。

## 产物

| 文件 | 说明 |
| --- | --- |
| `results/ablation.json` | 完整报告：每个变体的汇总、每个 (变体, 样本) 的原始输出与评分 |
| `results/thread-value.json` | 真实收藏上线程读取 vs 只读原帖的逐条对比 |
| `results/title-nondeterminism.json` | 对照实验：同一提示词多次运行的标题随机性 |
| `testdata/real-corpus.json` | 从 Worker 拉取的真实书签语料 |
| `results/identity.json` | 语料 URL 身份校验结果 |
| `results/ablation-run1-confounded.json` | 第一轮运行，保留作对照（语料标签未修正 + 并发较高） |
| `docs/ablation.md` | 结论与分析 |

## 已知限制

- 语料只有 4 条样本，足以发现“某个设计是否承重”，不足以给出精确的百分比。
- 端点并发能力有限，高并发会把变体推向超时，因此正式运行使用 `-concurrency 2`。
- 保真度只对 `source_only` 一类有可信参考文本的样本有意义；搜索类样本以结构、语言和
  自洽性信号为主，不对“精确字节”作断言。
