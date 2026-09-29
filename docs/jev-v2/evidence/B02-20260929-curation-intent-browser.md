# B02-T01/T02：原因、状态与标签确认的浏览器边界

日期：2026-09-29。真实 Chrome 驱动当前 Dashboard 资源；HTTP 使用受控本地夹具。Worker 另用 Miniflare/D1 测试真实路由。没有付费请求或远端部署。

## 旧缺陷与修复

`docs/jev-v2/archive/REVIEW-20260920.md` R23 记录旧 `reader.js` 的失败路径：未确认标签时，首次仅保存 why/status 也带整份 `classification`，Worker 将 AI 标签误记成人工确认。当前 `saveWhy` 仅提交 why，状态仅提交 `curation_status`；v1 标签编辑和显式确认才提交 classification，v2 编辑走独立 override。

本轮进一步修复两处确认竞态。v2 override 尚在途或其详情尚未回读时，确认按钮及快捷键不能提交旧 AI 投影；失败后放弃修改会重新读取详情。新显式确认携带 `expected_revision` 和稳定的 `operation_key`，Go Dashboard 校验并透传给 Worker。Worker 在事务守卫下确认三个 v1 字段，包含“值恰与 AI 建议相同”的情况；响应丢失后同键重放不再追加事件；别的客户端先改标签时旧版本确认返回 409 并由页面回读。旧客户端无这两个字段时继续走原兼容路径。

## 复验

- `make test-browser`：真实 Chrome 173/173。覆盖首次只保存原因、仅改状态、503 后保留原因草稿并重试、已确认后的字段独立保存、显式标签修改、确认、v2 在途竞态与冲突放弃、确认响应丢失后同键重放、另一客户端先提交后的旧版本冲突。逐条检查请求体和 DOM；模型与 X Search 调用为 0。
- Worker `npm test -- curation.test.ts`：15/15。直接调用 App 路由与真实本地 D1：确认与 AI 值相同仍留下人工确认和四条动作；同键重放不重复追加、异载荷冲突、另一客户端 v2 reject 后旧确认被拒绝。旧来源/人工值优先测试保留。
- Go `internal/dashboard` 的严格 JSON 校验和 `internal/cairn` 客户端测试检查两个新字段逐层透传。`make verify`、`make test-ablation`、Worker 全量测试、类型检查和 dry-run 的最终结果以提交对应的 CI/命令记录为准。

## 边界与发布顺序

浏览器矩阵没有直接连接 Worker；服务端合同在独立本地 D1 测试中验证。Go 新请求依赖 Worker 接受版本/操作键，因此发布顺序为先更新 Worker，再更新 Go。旧客户端仍可发送无版本的分类写入；跨旧新客户端和生产迁移的最终组合验收保留在 #16。#13 B06 的最终浏览器取证、真实使用观察与负载报告另行验收。
