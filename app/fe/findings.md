# Findings

- 现有页面为纯静态展示，未接入后端 API。
- `Dashboard` 仅触发页面跳转，未处理提示词提交与生成流程。
- `Workspace` 聊天、预览、代码均为本地 mock UI，未读取项目真实数据。
- `CodeEditor` 使用内置 mock 文件树，缺少按路径拉取文件内容逻辑。
- `API.md` 已统一为 `msg/code/data` 三字段，适合直接作为 API 客户端响应契约。
- 已新增 `src/api.ts`，覆盖 PRD MVP 接口并统一响应解析与错误处理。
- App 已接入“创建项目 -> 发起生成 -> 轮询任务 -> 进入工作区”流程。
- Workspace 已接入初始化并行加载：聊天历史、文件树、预览启动；并实现聊天发送与预览轮询。
- CodeEditor 已改为真实文件树驱动，并支持按路径异步加载代码内容。
- 已新增 Mockoon 环境文件：`mockoon/mvp-environment.json`，包含 MVP 闭环接口。
