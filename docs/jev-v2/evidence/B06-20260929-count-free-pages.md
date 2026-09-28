# B06 / D2：跳过未使用的列表计数（2026-09-29）

Go `BookmarkQuery.SkipCounts` 对 Worker 发送 `counts=0`；Dashboard 只接受 `0|1` 且拒绝重复值。Go 返回给页面时也省略计数字段。首屏和 overview/backstage 仍按旧合同请求计数；翻页、处理中的首屏轮询、导出分页、rerank 初读和复核改用无计数页。旧 Worker 忽略新参数时仍返回原合同，Go 和页面可继续读取。

本地 `make verify` 通过（含 race、lint、构建、488/488 前端检查），Chrome 130/130 通过。Go 测试覆盖客户端参数、Dashboard 无计数响应、默认兼容与导出翻页；Worker 34 文件、355/355 测试与 dry-run 通过，在真实本地 D1 上验证无计数 SQL 和游标。本地 Wrangler/D1↔Go 重启联调通过 Go 客户端和 Dashboard 的 `counts=0` 读取。新增付费调用 0。

这是 D2 的局部交付。同负载 D1 `rows_read`、单查询 overview 与 generation 缓存、8 小时整页内存及生产观察尚未验收。未部署、迁移或合并。
