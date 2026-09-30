# B04 独立分类命令的多维词表一致性 · 2026-09-23

修复实际入口差异：服务优先加载 v2 多维词表，而 `classify` 命令始终加载旧词表。因此，同一后端的正常多维目标会被 CLI 判为不支持并暂停。本批统一词表加载，推进 B04-T10/T11/T14/T15 与 B10 命令兼容工程部分；不代表 B04、B08 或整体验收完成。

受测 Enricher 源码 **`d451e2129305eae41b374cf3c0586bd218431151`**，配套 Share **`9571445910a5426a0a74654bdd59a9223fb158bc`**。本批无 Share 源码变化。

## 行为

- 服务与独立命令共用 `GetClassificationCatalog`：优先 v2，仅在端点返回 404/405 时加载旧词表。401/403、429、5xx、重定向及无效 schema 均保留错误，不静默改用另一个 spec。
- CLI 旧词表回退提示写 stderr，stdout 保持可解析 JSON。词表加载失败时，即便指定 `--id`，也不执行重试写入、保存 spec 或领取任务。
- 全维度问题使用与服务相同的 spec；没有更改问题语义、policy、模型配置或生产 target。有效旧后端的后续协议行为沿用原逻辑，HTTP 夹具不冒充完整旧部署兼容矩阵。

## 失败与验证

| 范围 | 证据 |
|---|---|
| 修复前 | [有效失败对照](logs/20260923-cli/b04-cli-catalog-baseline-final.log)：实际 Cobra 命令执行，v2 读取 0 次、旧词表读取 1 次，随后因多维目标 spec 不支持而暂停 |
| 命令边界 | [回归记录](logs/20260923-cli/b04-cli-catalog-boundaries.log)：多维目标及 11 个回退/错误场景通过，错误无写入、回退提示不污染 JSON |
| 完整检查 | [make verify](logs/20260923-cli/b04-cli-verify-final.log)：lint、全包 race/coverage、73 前端检查与构建通过 |
| 独立二进制 | [真实 CLI 场景](logs/20260923-cli/b04-cli-real-cli-final.log)：临时目录编译运行实际二进制，连接真实本地 Worker/D1/R2 与本地模型 HTTP 夹具 |
| 全部服务场景 | [11 场景](logs/20260923-cli/b04-cli-real-services.log)全部通过；保留既有十场景并加入实际 CLI |

实际 CLI 用两个不同的合成收藏，预先保存来源并注册多维目标：两次 `--max-jobs 1` 各保存一个完整 run/decision；第三次空队列返回 0 且无模型请求；显式 `--id` 重试再执行一次。总共 **3 次本地模型夹具调用**。断言完整 question population、spec/hash/policy、raw 与多维 decision 均保存，来源快照逐字段不变，明确人工选择的 contra 保留。子进程运行在只含测试二进制的临时目录，并使用显式本地环境，不会读取工作区 `.env`。

开发中的初次集成失败来自共用 POST helper 仅接受 200、拒绝合法创建响应 201；已修正该测试 helper。初次 lint 指出可变子进程参数：执行参数改为固定命令及类型化数字 ID，构建输出路径使用 `t.TempDir`，仅对这一处固定包构建添加有说明的 G204 例外。未关闭 lint 规则或删除业务断言。[初次集成](logs/20260923-cli/b04-cli-real-cli-first.log) / [初次完整检查](logs/20260923-cli/b04-cli-verify-first.log)保留。更早使用错误旧端点的探测不算产品缺陷证据。

## 范围与后续

[Enricher CI 35772075716](https://github.com/Alpenl/cairn-x-enricher/actions/runs/35772075716) 对上述精确源码 **SUCCESS**，覆盖 lint/race、前端、构建与多架构容器；[完整步骤](logs/20260923-cli/cairn-x-enricher-ci.json)已归档。未变更的 Share 沿用 [CI 35769767080](https://github.com/Alpenl/cairn-share/actions/runs/35769767080)，本批另外运行上述真实跨仓服务场景。

本批新增付费调用 **0**，累计仍 **127 次**。没有新的 dev/holdout 模型、拟合或质量使用；没有设备 instrumentation 重跑。未部署、执行生产迁移或切换 target。

该修复解决工程入口一致性，未改变此前自动参考训练的负结果，未提升任何质量门槛结论。无需人工标注，自动参考不冒充人工 gold。B08 的新 spec 质量评估、载体定义边界、潜在用途参考范围、校准/消融/检索/dev/冻结后 holdout，以及原 126 B、全部 R/SC、B07/B09/B10 与独立复审继续执行。两 PR 保持 Draft，原 issues 保持开放。
