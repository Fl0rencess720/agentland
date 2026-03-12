# Task plan

## Goal
实现 PRD 中的最小闭环并将 `PRD.md` 全部任务标记为完成。

## Phases
1. [complete] 评估现状与设计改造方案
2. [complete] 实现 API 层与应用级状态流转（创建项目、发起生成、轮询任务、路由）
3. [complete] 实现 Workspace 左侧聊天与右侧预览/代码联动
4. [complete] 补充 loading/empty/error 态与 Mockoon 环境文件
5. [complete] 运行构建检查并更新 PRD 任务状态

## Errors encountered
| Error | Attempt | Resolution |
| --- | --- | --- |
| `Property 'env' does not exist on type 'ImportMeta'` | 1 | 新增 `src/vite-env.d.ts` 并引入 `vite/client` 类型声明。 |
