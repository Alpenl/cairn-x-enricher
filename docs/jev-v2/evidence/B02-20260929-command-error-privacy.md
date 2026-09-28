# OBS-04：命令失败时的 stderr 私文边界（2026-09-29）

上轮已给 Go 的同步和异步 JSON 日志加字段、消息及分组过滤，但 `main()` 在任何子命令失败后仍把 `err.Error()` 原样写到 stderr。Docker 可以采集该输出；网络错误和命令参数可能包含私人 URL、来源文本或 token。该出口绕过 JSON handler 和运行时日志开关。

现在命令失败只写固定的安全错误类别，退出码仍为 1。命令根层不复制自由错误文本；可识别的 Worker HTTP、供应商 HTTP、取消、超时和既有错误类别仍通过安全码区分。回归把私人 token 放进未知命令名，调用与生产 `main()` 相同的执行/输出函数，断言 stderr 不含原值。`make verify` 覆盖 vet、lint、race、488 项前端检查和构建；无需真实供应商或远端环境。

这只覆盖 Go 命令根层 stderr。Worker/Web/Android、采集器、终端手动导出、Docker stdout 阻塞与丢弃、同负载开销和 14 天观察期仍按 #11/#16/#20 的 OBS-01–05 完整验收。未部署或合并。
