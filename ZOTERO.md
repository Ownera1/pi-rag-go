# Zotero metadata

rag-go 通过 Zotero Desktop 的 Local API 读取 metadata，并保存在每个 workspace 的 `.rag-go/catalog.db`。正文索引仍使用原来的 SQLite generation。`rag rebuild`、`rag clean` 不会删除 catalog。

第一版采用全量读取和内容哈希比较。Zotero 9 的版本号可能遗漏尚未同步的本地编辑，因此不使用 `since` 做增量；Zotero 10 也暂时走同一流程。读取、过滤和关联均不向 Zotero 写入数据。

接口约定见 [Zotero Local API 官方文档](https://www.zotero.org/support/dev/web_api/v3/local_api)。

## 开始使用

在 Zotero「设置 → 高级」开启“允许此计算机上的其他应用程序与 Zotero 通信”。Local API 的只读请求无需 API key。Zotero 必须运行；API 未开启时会提示设置位置，连接被拒绝时会提示启动应用。

从这份源码构建新的 CLI：

```sh
go build -tags sqlite_fts5 -o bin/rag ./cmd/rag
./bin/rag zotero sync --workspace /absolute/knowledge-workspace
./bin/rag zotero status --workspace /absolute/knowledge-workspace
./bin/rag zotero match --workspace /absolute/knowledge-workspace
```

workspace 需要已经执行过 `rag init`。metadata 同步可以先于正文索引执行，不调用 embedding 服务。

可在 `.rag-go/config.json` 增加以下可选配置；省略时使用这些默认值：

```json
{
  "zotero": {
    "baseUrl": "http://127.0.0.1:23119/api/",
    "libraryType": "user",
    "libraryId": "0",
    "startOnDemand": false
  }
}
```

这是要添加的配置字段，其他原有字段仍然需要保留。`user/0` 表示当前个人库，入库时解析为响应中的实际 user ID；群组库使用 `group` 和实际 group ID。客户端只接受 HTTP loopback `/api/` 地址。

`startOnDemand=true` 或 `rag zotero sync --start-zotero` 会在连接被拒绝时通过 macOS `open -g -a Zotero` 启动应用，并有限等待 API 就绪。403、超时、其他 HTTP 错误不会触发启动。其他系统需手工启动 Zotero。

临时选择其他库可用 `--library-type group --library-id GROUP_ID`，这些选项不会修改配置。长期使用某个群组库时将它写进配置，让随后新增文档的自动关联使用同一个库。

## 同步与删除

客户端读取所有 items（包括 trash）和 collections 的分页，检查对象 key、页大小、总数及库版本，并在结束时重新核对 key 集合。网络或数据验证失败时不应用该快照。网络读取完成后，条目、创作者、标签、collection 关系、删除差集和同步时间在同一个 catalog 事务里提交。

内容哈希只使用 item `data`，排除 `version` 和 `dateModified`，并规范化 tags/collections 的排列顺序。完整原始响应仍保存在 `raw_json`，版本变化也会更新原始缓存。哈希变化表示 metadata 变化，不会触发正文重新 embedding。

列表没有返回的对象使用软删除。Zotero 9 可能让文献继续引用已删除的 collection；客户端补读这些集合，404 则保留删除记录。关系仍可追溯，但删除的集合不参与过滤。

手工锁定关联不会被自动匹配覆盖或删除。父条目被删除后，检索结果的 `metadata.orphan` 为 true，status/sync 的 `orphans` 会提示受影响的 document key；恢复条目可自动恢复原来的关联。`rag zotero links` 输出关联及路径，方便核对和备份。

Zotero 9 的库版本不能检测所有本地编辑，分页核验也不是服务器提供的事务快照。如果同步期间持续修改条目，应在操作结束后再同步一次。正文同步和 metadata 同步是独立操作：`rag sync` 更新正文，`rag zotero sync` 更新 Zotero 快照。普通 query 使用已缓存的 metadata，并返回 `metadataSyncedAt`。

## 文档关联

关联使用独立的 document key，现有索引 document ID 和正文去重行为保持原来的规则：

- Manifest `sourceHash` 为有效 SHA-256 或 MD5 时，使用带算法前缀的原始文件哈希。
- 否则使用 canonical 内容文件字节的 SHA-256，排除 Manifest 的标题、路径和 Zotero 引用。
- 相同内容哈希共享文献关联；缺少原始文件哈希时，修改 canonical 内容会产生新的关联身份。长期保存解析产物时，应由转换流程填写原始文件 `sourceHash`。

每次成功的正文 sync/rebuild 会根据缓存重新做精确匹配，不访问 Zotero API。旧索引中的文档在 metadata sync/match 时补录稳定标识；只有源文件哈希仍与已索引内容一致时才补录，无需 embedding。文件已经改变时先执行正常的 `rag sync`。

匹配顺序为：保留已锁定关联、Manifest 显式引用、API 提供的准确附件路径、Manifest 中的唯一 DOI。附件需要关联到其 bibliographic parent。重复 DOI 不自动选择；`rag zotero match` 返回标题相似候选，但不会将候选写成关联。候选分数表示标题词重合度，不是概率。

Manifest 可增加可选的 `doi` 和 `zotero` 字段：

```json
{
  "version": 1,
  "format": "grobid-tei",
  "contentPath": "content.tei.xml",
  "sourcePath": "/Users/you/Zotero/storage/ATTACH01/paper.pdf",
  "sourceHash": "原始文件的实际SHA256十六进制字符串",
  "title": "Paper title",
  "doi": "10.1234/example",
  "zotero": {
    "libraryType": "user",
    "libraryId": "实际user ID",
    "itemKey": "PAPER001",
    "attachmentKey": "ATTACH01"
  }
}
```

示例中的哈希和 ID 需替换为实际值；item/attachment key 为 8 位大写字母或数字，`attachmentKey` 可省略。显式引用会被锁定。

手工确认：

```sh
./bin/rag zotero link documents/paper/rag-source.json PAPER001 \
  --attachment-key ATTACH01 --workspace /absolute/knowledge-workspace

# 同时把解析后的实际 library ID 和引用写入 Manifest，让它跟随文档：
./bin/rag zotero link documents/paper/rag-source.json PAPER001 \
  --write-manifest --workspace /absolute/knowledge-workspace

./bin/rag zotero links --workspace /absolute/knowledge-workspace
```

默认只保存 catalog 中的手工关联。`--write-manifest` 只允许修改 documents root 内的普通 `rag-source.json`，不写外部文件或 symlink。写入 Manifest 会改变原有输入指纹，随后正常正文 sync 可能重新 embedding；仅更新远端 metadata 不会如此。catalog 与 Manifest 是两个独立持久对象：若写文件失败，已经保存的 catalog 关联仍然保留。

catalog 包含人工状态，应随 workspace 备份。只有写入 Manifest 的显式关联才能在丢失 catalog 后通过重新同步和匹配恢复；不要将 catalog 当作可以随意丢弃的纯缓存。

## 检索过滤与 MCP

```sh
./bin/rag query 'channel estimation' --mode bm25 --no-sync \
  --year-from 2023 --year-to 2026 --tag ISAC --collection COLLECT1 \
  --workspace /absolute/knowledge-workspace
```

年份范围包含边界，未知年份不满足年份限制。重复 `--tag` 或 `--collection` 表示 AND；collection 使用 key，仅匹配该集合的直接成员。已删除条目、删除集合和未关联文档不满足 metadata filter。允许集合为空时返回零结果，不退回全库。

catalog 先筛 document key，再通过索引连接的 TEMP 表转换成 chunk rowid。英文 FTS、中文 FTS、向量 KNN 都在 top-k 截断前应用同一集合。主索引以 `mode=ro` 打开时仍可以使用 TEMP 表，持久表不能写入。

Go API 使用 `QueryOptions.Filter`。MCP `rag_query` 示例：

```json
{
  "query": "channel estimation",
  "mode": "hybrid",
  "disable_sync": true,
  "filter": {"year_from": 2023, "tags": ["ISAC"], "collections": ["COLLECT1"]}
}
```

命中结果的 `metadata` 包括 title、abstract、date、year、publication、DOI、citation key、按顺序保存的 creators、带类型的 tags、collection keys 和关联状态。机构作者使用 `name`；个人作者使用 `firstName`/`lastName`。元数据缺失的普通文档仍能通过不带 filter 的正文检索返回。

本地可写 MCP 增加 `rag_zotero_sync`、`rag_zotero_match`、`rag_zotero_link`；`rag_status` 包含 catalog 状态。只读 stdio 和 HTTP 仍只暴露 query/status/list，不提供写入或启动 Zotero 的工具。

## 验证记录

2026-10-06，本机 Zotero 9.0.6 Local API 实测 `/items`、`/collections`、key 列表及附件 URL 可以读取，`/deleted` 返回 404。隔离 workspace 同步了 140 条对象：57 条文献条目、59 个附件、24 个 annotation。重复同步为 0 条变化；5 个已删除 collection 被保留为删除记录，外键检查通过。

使用真实附件路径及原始 PDF 哈希，配合 synthetic 正文和本地固定 embedding，验证了准确附件关联、手工引用写入 Manifest、搬迁/rebuild 后锁定关系、BM25/vector 过滤、空过滤返回零结果，以及 metadata sync 不调用 embedding。此验证不代表真实 PDF 正文解析质量或真实 embedding 服务质量。

另外通过实际 stdio MCP 验证了 `rag_zotero_sync`、带年份 filter 的 `rag_query` 和 metadata 返回；只读 stdio 可以检索 metadata，同时拒绝 metadata 同步工具。

回归测试覆盖分页中断/变化、未同步编辑、无意义版本变化、403 与连接拒绝、catalog 事务回滚、删除/恢复、重复 DOI、中文过滤、旧索引补录、只读边界，以及 5000 个过滤 key 和 top-k 前过滤。
