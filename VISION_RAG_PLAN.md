# Retrieve-then-Augment-with-Vision 改造方案

## 项目背景

**目标**：基于 WeKnora 构建工艺专家系统 MVP，支持图文混合的工艺知识（PPT/Excel/PDF）的检索与推理。

**核心问题**：工艺文档大量依赖图文结合的表达方式，而 WeKnora 原生是纯文本 RAG 架构。docreader 在解析文档时将图片单独提取，VLM 对图片的描述与上下文语义脱节，信息损失严重。

**解决思路**：Retrieve then Augment with Vision
- 保留 WeKnora 的分块、向量检索、知识管理能力
- 预处理阶段将文档统一为 Markdown + 外部图片引用
- 检索命中文本块后，追溯其中的图片引用，将原图连同上下文一并送给多模态 LLM 推理

---

## WeKnora 架构关键信息

### 数据存储

| 表 | 关键字段 | 说明 |
|---|---|---|
| `knowledge_bases` | `id` | 知识库，URL 中可见 |
| `knowledges` | `id`, `file_path`, `knowledge_base_id` | 每个上传文件对应一条记录 |
| `chunks` | `id`, `knowledge_id`, `content`, `start_at`, `end_at` | 分块文本，含图片引用 |

**文件存储路径规则**（本地存储模式）：
```
容器内绝对路径：/data/files/{tenant_id}/{knowledge_id}/{时间戳}.md
local:// 表示：local://{tenant_id}/{knowledge_id}/{时间戳}.md
```

### 分块器对 Markdown 图片的处理

`internal/infrastructure/chunker/splitter.go:49` 中，`![...](...)` 格式的图片引用被列为**受保护模式**，不会在分块时被截断。分块后 chunk 的 `content` 字段会完整保留图片引用，例如：

```
...工件装夹时需注意以下事项：
![Figure](figures/MPP1_p001_figure_008.png)
主轴转速建议控制在...
```

`internal/infrastructure/chunker/splitter.go:557` 已提供 `ExtractImageRefs()` 函数，可从 chunk 文本中提取所有图片路径。

### 图片路径解析机制

`internal/models/chat/image_resolve.go:16` 中，`resolveImageURLForLLM()` 支持：

| 路径格式 | 处理方式 |
|---|---|
| `http(s)://` | 直接传给 LLM API |
| `data:...` | 直接传给 LLM API |
| `local://{rel_path}` | 从 `/data/files/{rel_path}` 读取 → 转 base64 data URI |
| 其他（相对路径等） | 原样返回，LLM API 会报错 |

### 多模态推理触发条件

`internal/application/service/chat_pipeline/common.go:87`：

```go
if chatManage.ChatModelSupportsVision && len(chatManage.Images) > 0 {
    userMsg.Images = chatManage.Images  // 图片随消息发给 LLM
}
```

`chatManage.Images` 目前只从用户上传的聊天图片填充，**不包含知识库图片**。这是需要新增的能力。

### Chat Pipeline 插件顺序

```
QueryUnderstand → Search → Rerank → Merge → IntoChatMessage → [LLM 推理]
```

新插件需插入在 `Merge` 之后、`IntoChatMessage` 之前。

---

## 当前进展

### ✅ 已完成

1. **架构分析**：完整梳理了 WeKnora 从上传、解析、分块、向量化到检索、推理的完整数据流

2. **可行性验证**：
   - 分块器原生保护图片引用不被截断 → chunk 内天然携带图片路径
   - `local://` 路径体系已有完整的文件读取 + base64 转换机制
   - `chatManage.Images` 注入点清晰，现有多模态推理链路可复用

3. **预处理文档格式确认**：
   - 使用 bytedance/dolphin 将文档转为 Markdown
   - 图片引用格式：`![Figure](figures/MPP1_p001_figure_008.png)`
   - alt text 统一为 `[Figure]`，**不影响任何环节**（路径才是关键）

4. **首个知识条目完成迁移**：
   - 文件：`front_piston_rod_1.md`
   - knowledge_id：`75db1990-6f14-4bde-9bfb-c15e97475107`
   - tenant_id：`10000`
   - figures 目录已复制至：`/data/files/10000/75db1990-6f14-4bde-9bfb-c15e97475107/figures/`
   - **重要**：`knowledge_id` 在重新解析后不会改变，可永久用于路径构建

### ⏳ 尚未完成

- `PluginVisionAugment` 插件（核心代码改动）
- Docker 镜像重建与部署
- 端到端功能验证

---

## 下一步行动计划

### Step 1：编写 PluginVisionAugment（核心改动）

**新建文件**：`internal/application/service/chat_pipeline/vision_augment.go`

**逻辑**：
1. 遍历 `chatManage.MergeResult`（检索命中的 chunk 列表）
2. 对每个 chunk 调用 `ExtractImageRefs(chunk.Content)` 提取相对路径
3. 通过 `chunk.KnowledgeID` 查出 `knowledge.file_path`（格式：`local://10000/{id}/{ts}.md`）
4. 截取目录前缀：`local://10000/{knowledge_id}/`
5. 拼接图片相对路径：`local://10000/{knowledge_id}/figures/xxx.png`
6. 追加到 `chatManage.Images`

**路径推导示意**：
```
knowledge.file_path = "local://10000/75db1990-.../1779936643147786670.md"
                                   ↓ 取最后一个 "/" 前的部分
目录前缀            = "local://10000/75db1990-.../"
图片相对路径        = "figures/MPP1_p001_figure_008.png"
                                   ↓ 拼接
完整 local:// 路径  = "local://10000/75db1990-.../figures/MPP1_p001_figure_008.png"
                                   ↓ resolveImageURLForLLM 自动处理
磁盘路径            = /data/files/10000/75db1990-.../figures/MPP1_p001_figure_008.png
```

**前提**：需要在 `SearchResult` 填充阶段带出 `knowledge.file_path`，或在插件中通过 `KnowledgeID` 做一次 DB 查询。

### Step 2：注册插件

修改 `internal/application/service/session_knowledge_qa.go`（或对应的 pipeline 初始化位置），在 `NewPluginMerge(...)` 之后添加：

```go
NewPluginVisionAugment(eventManager, knowledgeRepo)
```

### Step 3：确认模型配置

在 WeKnora 前端的模型配置中，将推理模型设置为支持视觉的多模态模型（如 GPT-4o、Qwen-VL、claude-sonnet-4-6 等），确保 `ChatModelSupportsVision = true`。

### Step 4：重建镜像并测试

```bash
docker compose build app
docker compose up -d app
```

验证方式：上传一个含图片引用的 Markdown 文档，在聊天中提问与图片相关的工艺参数，观察 LLM 是否基于图片内容给出回答。

---

## 已知边界与后续优化方向

| 问题 | 现状 | 后续方向 |
|---|---|---|
| 前端图片预览显示为断链 | 预期现象，浏览器无法解析 `figures/` 相对路径 | 可选：在 `/files/` 端点增加知识条目级别的静态资源路由 |
| 多图 chunk 无法区分图片位置 | LLM 收到多张图片但文本中无序号区分 | 预处理阶段加图片编号，或在插件中保留位置信息 |
| figures 需手动 docker cp | 每次上传新文档都需要操作 | 后期可在预处理脚本中通过 WeKnora API 或直接写卷完成自动化 |
| 重新解析不改变 knowledge_id | ✅ 已确认，路径稳定 | — |
