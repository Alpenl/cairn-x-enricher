# 浏览器与流程性能对照

2026-09-12 的结果保存在 `workflow-results.json`、`browser-results.json` 和对应原始文本中。
基线来自任务开始时的工作区副本，包含当时已有的未提交改动，不是单纯的 Git HEAD。

工作流使用同一个固定生成器和词表，在 Linux amd64 / Go 1.26.8 下运行五轮
`go test ./internal/enrich -run '^$' -bench BenchmarkWorkflow -benchmem -count=5`。
两侧只改变编排实现：Eino 两节点与普通 Go 顺序调用。完整生成器见
`internal/enrich/workflow_bench_test.go`；不请求网络或模型。二进制使用相同的
`CGO_ENABLED=0 go build -trimpath -ldflags='-s -w'` 参数构建，模块数量由
`go list -m all` 得到并排除主模块。构建耗时受本地缓存影响，只归档原始值，不作为收益结论。

浏览器使用真实 dashboard HTML/CSS/JS 和 `preview.py` 的固定数据服务。
80 条模拟收藏，每页 40 条，每条原文/译文各 240 段。模拟 API 支持与 Worker 集成测试一致的
`view=summary` 投影，数据不包含真实收藏、凭据，也不访问生产数据库。

分别在两个终端运行：

```bash
shnote --what "启动基线页面" --why "提供优化前的静态资源" run python3 experiments/performance/preview.py --assets /path/to/before/internal/dashboard --port 8765
shnote --what "启动当前页面" --why "提供优化后的静态资源" run python3 experiments/performance/preview.py --port 8766
```

安装好 `agent-browser` 后采样：

```bash
shnote --what "测量浏览器前后变化" --why "比较响应大小和节点复用" run python3 experiments/performance/measure.py
```

脚本为两侧创建独立浏览器会话，等待首批收藏到达，观察 11 秒列表轮询和 9 秒详情轮询，
检查正文/图片节点是否保留，然后展开原文并检测 390×844 视口的横向溢出。
列表字节数使用 Resource Timing 的 `decodedBodySize`，是未压缩响应体大小；不能据此
推导实际生产网络流量、模型调用速度或端到端首屏时间。各轮机器负载也可能影响耗时。

最新结果：列表响应 `4,708,973 → 33,053` 字节；阅读页初始 DOM `574 → 334`；
折叠原文 `240 → 0` 段，展开后两侧均为 240 段；新版轮询保留原有正文及图片节点。
