# 数据状态演示 URL

验收加载、错误和空数据状态时，在开发或预览地址后添加 `demo` 参数：

- `?demo=loading`：持续显示初次加载状态。
- `?demo=error`：显示可重试的读取错误。
- `?demo=empty`：显示数据源尚无历史事件的空状态。
- `?demo=performance`：载入 10,000 条确定性历史事件，用于性能验收。

移除 `demo` 参数即可恢复默认 HTTP Repository，并从 Go 服务读取 PostgreSQL 数据。
