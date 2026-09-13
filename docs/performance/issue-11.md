# Issue #11：万级数据性能验收

## 结论

保留 SVG 图形层与 HTML 交互层。当前实现已达到目标，不需要切换到 Canvas。
时间线布局继续由 `layoutTimelineEvents` 产出与绘制方式无关的
`TimelineEventPresentation`，SVG 仅由 `TimelineGeometry` 消费；以后若数据或设备基线
变化，可在不改动布局和无障碍交互层的情况下替换图形层。

## 可重复场景

- 数据：`createPerformanceEvents()` 生成固定的 10,000 条历史事件，无随机输入；其中
  包含一个 120 条同年事件的密集区域，用于验证大型聚合簇。
- 页面：生产构建的 `?demo=performance`。
- 命令：`npm run benchmark:performance`。
- 浏览器：无头 Chrome 153，桌面视口 1440 × 1000，移动视口 390 × 844。
- 环境：macOS 26.6.2，Node.js 26.7.0。
- 原始记录：[`issue-11-results.json`](./issue-11-results.json)。

脚本通过 Chrome DevTools Protocol 重放筛选、连续拖拽和连续滚轮缩放；使用
`requestAnimationFrame` 记录帧率与 P95 帧间隔，使用 Long Tasks API 记录长任务，
并通过 MutationObserver 记录历史事件交互节点的峰值。

## 2026-09-12 结果

| 指标 | 目标 | 结果 |
| --- | ---: | ---: |
| 主分类筛选响应 | < 100 ms | 33.6 ms |
| 连续拖拽 | 50–60 FPS | 60.16 FPS；P95 16.8 ms |
| 连续缩放 | 50–60 FPS | 60.00 FPS；P95 16.8 ms |
| 持续长任务（≥ 100 ms） | 0 | 0 |
| 常态历史事件 DOM 元素 | ≤ 约 300 | 5 |
| 122 条密集事件展开后的事件 DOM 元素 | ≤ 约 300 | 229（列表单页 40 条） |
| 桌面/移动水平溢出 | 0 px | 0 px / 0 px |

筛选、拖拽和缩放场景均遍历 10,000 条源数据。宽时间尺度下，Repository 按固定
事件显著度返回事件，布局层再将屏幕空间中的密集事件压缩为聚合簇，因此常态只需
少量 HTML 交互节点。聚合簇悬浮预览最多渲染 12 条；最大缩放下的重叠列表每页渲染
40 条，并提供上一页/下一页按钮，使全部可见及可交互内容保持键盘可达。

## 关键优化

1. Repository 在初始化时预计算时间范围和规范化搜索文本，查询时单次遍历数据源。
2. 聚合布局不再在每加入一条事件时重复排序或拼接全部 ID，避免密集簇的二次复杂度。
3. 大型聚合簇采用限量预览和分页交互，限制常态 DOM，同时保留完整访问路径。
4. SVG 绘制提取为独立 `TimelineGeometry`，维持布局、绘制和 HTML 无障碍层边界。
