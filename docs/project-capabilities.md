# 工作区五个项目的能力与迁移边界

2026-10-01 按实际目录、生产装配和验证入口整理。源码已有功能、测试通过和生产上线是三种不同状态；发布证据另外记录完整提交、镜像、迁移及验收结果。

| 项目 | 真实职责 | 数据与运行方式 | 验收入口 |
| --- | --- | --- | --- |
| `cairn-share` | Android 分享、收藏阅读/整理、持久操作队列；Worker 为当前收藏数据权威，保存标签事实、lease、预算与审计 | Cloudflare Worker + D1 + 私有 R2；Android 独立签名 APK | Worker test/typecheck；Android unit/lint/build/instrumentation；只读生产合同验证 |
| `cairn-x-enricher` | NAS Reader、来源获取、阅读增强、Jev 分类、人工标签代理、离线评估 | 单 Go 服务，经内部 token 访问 Share；模型仅后台受控阶段调用 | `make verify`；固定 Worker 版本的真实集成与 Chrome；部署后时延及队列状态 |
| `cairn` | 完整自托管阅读系统：采集、Reader PWA、扩展、Android/iOS、RSS、笔记、划线、翻译、归档 | Go + PostgreSQL/River；安装 token 和 Reader session | 自身 `Makefile`、真实 PostgreSQL 与各客户端门禁；现有用户改动单独保留 |
| `cairn-backend` | Core + RSS 两进程重写，兼容旧 Cairn 公共合同；安装、幂等、队列和导入工具 | 隔离的 Core/RSS PostgreSQL schema，RSS 内部 HTTP 合同 | `make gate` / `make full-gate`、contract、differential、client-acceptance |
| `cairn-reader-static` | React/Vite 阅读器视觉与交互预览 | 静态模拟数据，无真实服务写入或同步 | TypeScript/Vite build；不能代替端到端验收 |

## 当前收藏链路

Share App → Worker/D1 是收藏入口；NAS 读取与增强使用同一份事实。`cairn` 和 `cairn-backend` 并未因为目录相邻就自动参与这条线上链路，静态 Reader 也不是 NAS 页面。

自动标签、人工增量、当前有效标签分开持久化。三个主要维度为主题、资源类型和内容特征；自定义标记可选。评估必须区分真实人工标注、模型参考及来源不明历史，不能将“从未修改”当作正确答案。

## 重写后端的 AI 与翻译

`cairn-backend/internal/core/modelprovider` 已提供可配置 Chat Completions 适配器，并由 `internal/core/app/production.go` 注入 Reader AI 与翻译处理器。未配置时保留明确的不可用响应；配置错误拒绝启动，不选择隐式模型。RSS 不接收这些配置或密钥。

本地模拟 HTTP 覆盖实际请求、取消、超时约束、错误响应、截断、超长响应、拒绝重定向和受保护 Markdown 翻译。它证明适配器与业务合同，不证明真实供应商质量或费用；本轮不调用真实模型，也不把 Core/RSS 替换成线上 Share 后端。

重写工作树尚无初始提交。全量检查发现原有幂等测试仍期待不同请求体产生相同指纹，而实现已采用正文指纹；另有原有 lint 问题。因此不能宣布重写后端全量门禁已通过。新增提供者的定向测试通过；保留原有实现，不为了绿灯回退幂等校验。

## 迁移验收

只有完成下列证据才可以把某个旧模块标为已替代：

1. **合同**：固定旧 API/客户端版本；校验状态码、错误、鉴权、排序、分页、幂等、CAS 和空值语义。
2. **数据**：演练导入、重复导入与部分失败恢复，比较 ID、内容、人工事实、时间、来源版本和引用；先保留可恢复快照。
3. **后台任务**：进程重启、lease 过期、未知外部调用、超时/取消、重试与账本不能丢失或重复执行。
4. **客户端**：实际 Reader/扩展/Android/iOS 使用真实后端走完关键流程；静态预览和 OpenAPI 类型检查不足以替代。
5. **发布与恢复**：验证精确镜像/提交、数据库版本及只读线上合同；明确旧版本可恢复范围和切换后新增数据的保留方式。

本轮优化继续使用已有生产拓扑，不进行跨系统数据库迁移，也不删除旧客户端或未提交的重写成果。
