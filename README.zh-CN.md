# rag-go

[English](README.md) | [简体中文](README.zh-CN.md)

rag-go 是面向 Agent 的本地检索引擎。它将文档正文解析成结构化内容块，完成分块和 embedding，并通过 SQLite FTS5 与 sqlite-vec 提供关键词、向量及混合检索。每个工作区独立保存配置、索引和可选的 Zotero metadata；多个 Agent 进程通过操作级锁共享同一个工作区。

PDF 提取、OCR 和版面恢复由 MinerU Desktop 等外部工具完成。rag-go 读取转换后的 Markdown、结构化 JSON 或 XML，并保留已有的来源、章节和页码信息。

## 快速开始

通过 Homebrew 安装已发布的 macOS/Linux 原生二进制。安装一次，之后每个项目只需初始化：

```sh
brew install --formula ownera1/tap/rag-go
rag install    # 一次：embedding 服务、隐藏输入的 API key、接入已安装的 Agent
cd my-project
rag init       # 不提问；./documents 中已有文件时直接建立索引
```

执行 `rag install` 后重启 Agent，Agent 即可检索它所在的项目；在终端中 `rag query '信道估计'` 同样可用。查询会自动同步变更的文档，无需单独的索引步骤。

`rag install` 将全局默认配置保存到 `~/.config/rag-go/config.json`，API key 保存到同目录的 `credentials.json`（权限 `0600`；可用 `XDG_CONFIG_HOME` 或 `RAG_GO_CONFIG_DIR` 修改位置）。它先验证 embedding 服务，再注册一个不绑定工作区的 MCP 服务器 `rag mcp`：Claude Code 使用 user scope，Codex 写入 `~/.codex/config.toml`，只改动 `[mcp_servers.rag-go]` 表，其他设置和注释保持不变；Claude Desktop、Antigravity（IDE 与 `agy` CLI 共用 `~/.gemini/config/mcp_config.json`）和 pi（`~/.pi/agent/mcp.json`）只改动各自 JSON 中的 `mcpServers.rag-go`。自动检测按 CLI 或配置目录是否存在，在终端中会显示一个预先勾选已检测 agent 的清单；`--agents claude,codex,claude-desktop,antigravity,pi` 或 `--agents none` 可覆盖。Claude Desktop 的普通对话没有项目目录，提问时需给出工作区路径。重复执行是安全的；`rag uninstall [--purge]` 可撤销。Voyage 默认模型为 `voyage-4-lite`，维度为 1024。使用 OpenAI-compatible 服务时，需要填写对应的模型、地址和维度：

```sh
rag install --embedding-type openai --model YOUR_MODEL --dimensions YOUR_DIMENSIONS \
  --base-url http://127.0.0.1:11434/v1 --api-key-env=
```

`rag init [workspace] --docs PATH` 默认使用当前目录作为工作区，文档目录默认为 `./documents`。新工作区会复制一份全局默认配置，之后修改全局配置不会让已有索引失效。也可以指向外部文档目录，例如每篇论文各占一个文件夹的 MinerU 输出：

```sh
rag init ~/Projects/my-project --docs /absolute/converted-papers
```

未执行 `rag install` 时，`rag init` 会询问文档目录、embedding 服务、模型及 API key（不回显），并把 key 保存在工作区；它接受与 `rag install` 相同的服务参数。凭据依次从环境变量、工作区、全局文件读取。环境变量和全局文件只发给 Voyage 默认地址或 `rag install` 记录的地址，克隆仓库中的工作区配置无法把它们转发到别处；其他地址只使用 `rag init` 保存在该工作区的 key。`--no-sync` 跳过初始索引；`--offline` 跳过在线 embedding 探测和初始索引。重复初始化保留已有设置，只更新显式指定的选项；配置损坏时会报错。修改文档根目录、embedding 或分块配置后，需要执行 `rag rebuild`。

## 工作区与同步

```text
my-project/
├── documents/
└── .rag-go/
    ├── config.json
    ├── credentials.json        # 可选，私有凭据
    ├── state.json
    ├── catalog.db              # 可选，持久 Zotero metadata 和文档关联
    ├── .lock
    ├── rag.db                  # 首次 sync 创建
    ├── active.json             # rebuild 发布当前索引指针
    └── indexes/                # 保留的索引版本
```

命令从当前目录向上查找最近的 `.rag-go/config.json`。`--workspace PATH` 精确选择指定工作区；嵌套工作区相互独立。`.rag-go` 内置的忽略文件将配置、凭据和索引排除在 Git 之外，文档源文件由用户自行管理。

常用操作：

| 命令 | 用途 |
| --- | --- |
| `rag sync` | 扫描文档根目录，同步新增、变化和删除的正文 |
| `rag query '问题'` | 默认混合检索，并检查需要同步的文档 |
| `rag query '问题' --no-sync` | 直接查询现有索引 |
| `rag status` | 查看工作区、索引和可选的 Zotero catalog 状态 |
| `rag rebuild` | 构建新索引并原子切换，失败时保留原索引 |
| `rag clean --keep 3` | 预览旧索引清理；加 `--confirm` 才执行删除 |
| `rag tui` | 交互式面板：查看索引状态、编辑并保存工作区配置、带进度条运行 sync / rebuild / clean |

`rag sync` 递归扫描单一文档根目录，并对 canonical 内容及 Manifest metadata 计算哈希。未变化的文档跳过解析和 embedding。每个成功替换在事务内完成；失败文档保留原有 chunks。只有扫描完整、成功且稳定时才应用删除差集。处理期间发生变化的文件留待下次同步。

查询最多触发一次同步，并返回 `freshness`（`fresh`、`stale` 或 `unknown`）、可选同步报告及 `syncError`。同步失败时，兼容的已有索引仍可提供结果，同时说明失败；首次索引失败、索引不兼容或调用取消则返回错误。相同失败输入的自动重试冷却期为 60 秒，跨进程重启保留；输入变化或手动 `rag sync` 会立即重试。

每次操作重新加载配置和当前索引。读取持共享锁；同步、重建、初始化和清理持独占锁。空闲 MCP 进程不持有数据库连接或锁。项目没有文档 watcher 或后台常驻服务。

`rag rebuild` 只有在完整扫描及向量覆盖检查成功后才发布新索引。`rag clean` 保留当前索引、未知文件及 symlink，并删除中断的 rebuild 残留的 staging 数据库；`--dry-run` 始终只预览。只读连接可能创建 SQLite WAL 辅助文件，但不会修改持久内容、索引或状态。

## 文档格式与来源

| 输入 | 正文内容 | 位置依据 |
| --- | --- | --- |
| Markdown / MDX | CommonMark 标题、段落和代码；`$$` 行间公式保持完整，并与引出它的句子同在一个 chunk；图片链接只保留 alt 文字，单行 HTML 表格只保留单元格文字；跳过 References/Bibliography/参考文献 章节 | Markdown 行号 |
| TEI XML | 摘要、正文和附录，排除图表、公式、参考文献等子树 | 明确的 GROBID 坐标提供物理 PDF 页码 |
| JATS XML | 摘要及嵌套正文章节 | 页码未知 |
| MinerU JSON | 正文、标题和列表，保留行内 `$…$` 与行间 `$$…$$` 公式、算法块，图表标题，以及按行保留、单元格以 ` | ` 分隔并独立成块的表格；支持 content list v1/v2、middle JSON 和 structured content；跳过参考文献及页眉、页脚、页码、脚注；以句号结尾的标题（如 `Proof: See Appendix A.`）按正文处理 | 将显式 `page_idx` 从零基转成一基页码 |
| `.rag-blocks.json` | Version 1 规范化正文块 | 经校验的显式页码范围 |
| DOCX | 可见段落、标题、列表和表格单元格文字 | 页码未知 |
| HTML | main/article/body 文字及基础隐藏、导航过滤 | 页码未知 |
| TXT、代码及其他支持的 UTF-8 文本 | 纯文本 | 文本行号 |

一个文档包选择一个 canonical 表示。`rag-source.json` 优先；可识别的 MinerU 文件夹选择一种 JSON 表示，排除 Markdown、metadata 和其他伴随文件。混有多篇论文且无法确定边界的 MinerU 目录会报错并要求拆分。普通文件仍作为独立文档处理。文档包覆盖其子目录，因此文档根目录下直接出现的包文件会使扫描报错，而不是隐藏其他所有文档；扫描忽略 PDF 等不支持的资源。

要更正 OCR 错误而不改 MinerU 原件，在文档包目录下放一个 `rag-fixes.tsv`，每行一对 `错误文本<Tab>正确文本`（`#` 开头为注释），按顺序替换解析后的正文。错误文本直接从查询结果复制，LaTeX 反斜杠照写原样。某行一处都没匹配到时，该文档解析失败并报出行号，避免拼错或因 MinerU 重跑而过期的更正被悄悄忽略。修改该文件会触发重新索引。

可选的来源 Manifest：

```json
{
  "version": 1,
  "format": "markdown",
  "contentPath": "content.md",
  "sourcePath": "original.pdf",
  "title": "Research Paper"
}
```

`contentPath` 在 symlink 解析后仍须位于文档包内。`sourcePath` 只用于来源归属，rag-go 不解析或要求原始 PDF 存在。Markdown 本身不能恢复 PDF 页码；如需手工修改 MinerU 的 `full.md` 又保留页码，在 MinerU 文件夹的 `"format": "markdown"` manifest 中加入 `"pagesFrom": "layout.json"`（或某个 `*_content_list.json`）。每个 Markdown 段落按文字在该导出中定位并取其页码，手工修改不影响页码；导出中没有的文字沿用前后段落的页码；匹配段落不足 70% 时该文档解析失败。MinerU 会把延续到下一页的段落整段记在起始页；`layout.json`（MinerU Desktop）或 `*_middle.json` 标记了被挪动的行，用它时这类段落的页码范围覆盖两页。直接按 content list 索引的 MinerU 文件夹，若旁边有 `layout.json` 或 `*_middle.json`，也会同样修正页码。未知位置保持为空。当前检索范围是论文正文，公式、图像、图表关系检索不在范围内。解析输入上限为 64 MiB，超限的受支持文件会明确报错。

## Zotero metadata

Zotero 集成是可选功能，通过 Desktop Local API 只读访问个人库或群组库。metadata 保存在独立的 `.rag-go/catalog.db`，`rag rebuild` 和 `rag clean` 保留它。metadata 同步采用全量读取及内容哈希比较，更新 metadata 本身不会重新 embedding 正文。

在 Zotero「设置 → 高级」开启“允许此计算机上的其他应用程序与 Zotero 通信”，保持应用运行。在已初始化的工作区执行：

```sh
rag zotero sync
rag zotero status
rag zotero match
rag zotero links

# 手工确认关联；item key 替换为真实值：
rag zotero link documents/paper/rag-source.json PAPER001 --write-manifest

# 在召回前按 metadata 过滤：
rag query 'channel estimation' --mode bm25 --no-sync \
  --year-from 2023 --year-to 2026 --tag ISAC
```

默认连接 `http://127.0.0.1:23119/api/` 的 `user/0`，并从响应解析实际个人库 ID。群组库、连接地址和 macOS 按需启动配置见 [Zotero 使用说明](ZOTERO.md)。正文同步与 Zotero 快照同步是两个独立操作；普通查询使用缓存的 metadata。

关联优先保留手工锁定结果，再处理 Manifest 显式引用、经 API 验证的附件路径、附件文件名及唯一 DOI。没有 Manifest 的 MinerU Desktop 输出目录（`<原文件名>-<uuid>`）去掉 uuid 后缀后，与 Zotero 附件文件名精确比对，唯一命中即自动关联。标题相似匹配只生成候选。稳定 document key 优先使用 Manifest 中有效的原始文件 `sourceHash`，否则使用 canonical 文件内容哈希。锁定关联在条目被删除后保留并标记孤儿；写入 Manifest 的引用可随文档迁移。

检索结果顶层的 `documents` 按文档 id 给出每篇命中文档的 `version`，已关联时附带 `metadata`（title、abstract、date/year、publication、DOI、creators、tags、collections 及关联状态），同一篇文档只出现一次；每个命中以 `document` 字段指向它。年份、标签和 collection 过滤在 BM25、中文 FTS 和向量检索的 top-k 截断前生效。重复标签或 collection 条件表示 AND，collection 仅包含直接成员；空匹配集合返回零结果。

catalog 包含人工确认状态，应随工作区备份。完整 schema 行为、软删除保护、手工关联和过滤示例见 [ZOTERO.md](ZOTERO.md)。

## MCP 与 Agent 接入

```sh
rag mcp                                         # 本地 stdio，10 个工具，每次调用确定工作区
rag mcp --workspace /absolute/project           # stdio，固定一个工作区
rag mcp --read-only                              # stdio，5 个只读工具
rag mcp --transport http --listen 127.0.0.1:7331   # 前台运行，只读 HTTP
```

本地可写 MCP 提供 `rag_query`、`rag_read`、`rag_outline`、`rag_status`、`rag_list_documents`、`rag_sync`、`rag_rebuild`、`rag_zotero_sync`、`rag_zotero_match` 和 `rag_zotero_link`。查询参数支持 `query`、`mode`、`top_k`、`candidate_top_k`、`alpha`、`disable_rerank`、`require_rerank`；`disable_sync=true` 禁用正文自动同步，`filter` 在召回前应用缓存的 Zotero metadata 条件，`document`（文档 id、路径，或路径/标题中唯一的片段）把召回限定在一篇文档内。

Agent 可以借助三个读取工具越过检索片段连续阅读论文；它们只读索引，不同步、不调用模型。`rag_list_documents` 返回每篇文档的 `id`、`title`（已关联时用 Zotero 标题）、`path` 和 `version`。`rag_outline` 以 chunk 序号区间和页码列出章节，文档目录中恰有一个 PDF 时一并返回其路径。`rag_read` 按文档顺序返回 chunk，可用命中的 chunk id（`<文档 id>-<序号>`）配合 `before`/`after`（默认各 2）定位 `around`，或按 `from`/`to` 序号区间、按 PDF 物理页 `pages`（如 `8-9`）读取；输出不超过 `max_tokens`（默认 4000，上限 16000），被截断时给出续读起点 `next`；传入的 `version` 与当前不符时直接报错，不会读到错位的 chunk。`mode=literal` 忽略空白做原文匹配并按文档顺序返回，`\tag{28}` 即可定位公式 (28)。MCP 服务的 instructions 会把这套阅读流程告知所有客户端。命令行对应 `rag list`、`rag outline 文档`、`rag read --document … --from/--to/--around/--pages` 和 `rag query --document … --mode literal`。

不带 `--workspace` 时，stdio `rag mcp` 可在任意目录启动，每次调用都按必填的 `workspace` 工具参数确定工作区：Agent 当前项目的绝对路径，或其中任意子目录。调用不携带工作目录，而服务自身的目录在 Agent 切换项目后仍停留在启动位置，因此不设默认值，缺省或相对路径会被拒绝。在工作区之外调用会提示如何初始化。带 `--workspace` 启动的服务和所有 HTTP 服务只服务一个工作区：此时 `workspace` 可省略，便于不知道本机路径的远程客户端调用；若传入则必须是绝对路径且位于该工作区内。

只读 stdio 和所有 HTTP 服务仅暴露 query/read/outline/status/list，拒绝写工具并关闭自动同步。查询仍可调用配置的 query embedding 或 reranker。HTTP 仅监听 loopback，保留 Host/Origin 校验，将地址打印到 stderr；不指定端口时使用临时端口。远程隧道可接入只读 stdio 或可选 HTTP 服务，隧道安装与账号配置由外部工具管理。

```sh
rag connect claude
rag connect codex
```

通常用 `rag install` 接入 Agent；`rag connect claude|codex` 则只为单个项目注册：Claude 注册通过其 CLI 在工作区目录以 local scope 完成。Codex 注册只改动项目 `.codex/config.toml` 中的 `[mcp_servers.rag-go]` 表，其他设置、服务和注释保持不变；符号链接的配置文件会通过链接原地修改。两者都固定二进制和工作区的绝对路径，重复相同注册保持原状；冲突配置需加 `--replace`。Codex 仅为受信任项目加载项目配置。连接后重新加载 Agent；注册成功与 Agent 实际调用工具是不同的验收步骤。

## 配置与检索

配置示例见 [config.example.json](config.example.json)。`documents` 默认相对工作区解析，也可使用绝对路径。`excludePatterns` 使用 Gitignore 风格匹配，扫描默认排除隐藏目录、构建/缓存目录和 `.rag-go`。

对可信地址，环境变量凭据优先于 `.rag-go/credentials.json`；其他地址只接收工作区凭据。凭据直接传给 provider，不修改进程环境。存储目录权限为 `0700`，凭据和 JSON 状态权限为 `0600`；配置、索引和 status 输出不保存或打印密钥。

Embedding 支持 Voyage，以及接收 `POST {baseUrl}/embeddings` 并返回 `data[{index,embedding}]` 的 OpenAI-compatible 服务。可选 rerank 支持 Voyage 或通用 HTTP `POST {baseUrl}/rerank`，请求为 `{model,query,documents,top_n}`，响应为 `results[{index,relevance_score}]`。`none` 禁用 rerank。

切块前会合并同一章节、同一页内相邻的正文 block，使 MinerU 等按段落导出的格式切出接近目标大小的块，且不会扩大页码范围。默认的 `semantic` 切块会先为每个句子级单元生成 embedding 来选择边界，再为最终的块生成 embedding，因此索引消耗的 embedding tokens 约为 `legacy` 的两倍。`indexing.embeddingWorkers`（默认 4）限制同时进行的文档 embedding 请求数。

查询模式为 `hybrid`（默认）、`bm25`、`vector` 和 `literal`；`--no-rerank` 可用于基线测试。Hybrid 在查询 embedding 暂时失败时降级为 BM25，并返回 `method`/`degraded`；vector 模式返回错误。调用取消会中止操作。BM25 命中任一查询词即可召回，中文 BM25 使用汉字 unigram/bigram 索引。Hybrid 使用加权倒数排名融合（RRF，k = 60）合并 BM25 与向量两路排名；alpha 是 BM25 排名的权重，1 - alpha 是向量排名的权重。结果中的 `bm25` 和 `vector` 为原始 BM25 相关度与余弦相似度，`hybrid` 为融合分数。

默认 alpha 为 `0.4`，candidate top K 为 `30`，top K 为 `5`，语义分块阈值为 `120/280/420/140`。查询 usage 记录逻辑调用次数及估算 tokens，不包含重试和正文同步成本；同步报告单独记录索引工作。

## 源码构建与验证

构建需要 Go 1.25+、CGO 和 C 编译器。发布的二进制运行时无需 Go 工具链或 PDF 引擎。构建前先导出 SQLite 头文件参数，使 sqlite-vec 按实际链接的 SQLite 编译，而不是使用较旧的系统头文件：

```sh
git clone https://github.com/Ownera1/rag-go.git
cd rag-go
export CGO_CFLAGS="$(scripts/cgo-flags.sh)"
go build -tags sqlite_fts5 -o bin/rag ./cmd/rag

# 以源码构建的 CLI 初始化工作区：
./bin/rag init /absolute/my-project --docs /absolute/converted-papers
./bin/rag sync --workspace /absolute/my-project
./bin/rag query '信道估计' --workspace /absolute/my-project
```

测试和发行包检查：

```sh
go test -race -tags sqlite_fts5 ./...
go vet -tags sqlite_fts5 ./...
./scripts/package.sh v0.3.0-local dist
mkdir -p /tmp/rag-go-extracted
tar -xzf dist/rag-go_v0.3.0-local_$(go env GOOS)_$(go env GOARCH).tar.gz -C /tmp/rag-go-extracted
python3 scripts/smoke-release.py /tmp/rag-go-extracted
python3 scripts/test-release.py
```

Smoke suite 使用解压后的实际二进制和固定的本地 HTTP provider，检查独立查询、自动同步、FTS5/sqlite-vec、来源页码、stdio/HTTP MCP、重建、清理及四种评估模式。多进程测试覆盖共享读取、串行写入、锁取消和重复 embedding 防护。Zotero 的实测记录见 [ZOTERO.md](ZOTERO.md#验证记录)。这些检查验证本地行为，真实 provider 质量、费用、Agent 调用及跨平台发行另行验收。

发布保留 macOS arm64 与 Linux arm64/amd64 原生压缩包、SHA-256 校验和预编译 Homebrew Formula。压缩包包含 `rag`、LICENSE 和 README。脚本安装器只安装 `rag`，校验失败时保留原有二进制。

## 评估与 Go API

```sh
rag eval --dataset evaluation/sample/questions.jsonl --modes bm25,vector,hybrid \
  --output evaluation/runs/sample.json
```

评估直接调用 Core，需要已同步且兼容的索引，运行期间不会自动同步。报告包含 Recall@K、MRR、p50/p95 延迟、失败、降级、usage 及可选费用估算。样例数据为合成数据，详见 [评估说明](evaluation/README.md)。

`pkg/rag` 提供 `Open(Options{WorkspaceDir, ReadOnly, Embedder, Reranker})`，以及 `Core.Sync`、`Query`、`Documents`、`Read`、`Outline`、`Status`、`ListDocuments`、`Rebuild`、`Cleanup`、`SyncZotero`、`MatchZotero`、`LinkZotero`、`ZoteroStatus`、`ZoteroLinks`、`Close` 和 `DefaultConfig`。操作接收 `context.Context`；注入的 provider 须支持并发调用。Core 不依赖 MCP 或外部文档提取工具。
