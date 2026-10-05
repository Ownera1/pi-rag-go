# Migrating v0.1 to v0.2

v0.2 changes the store boundary, command surface and MCP registration. Existing Go/TypeScript stores and the original source documents stay in place. v0.2 initializes a separate workspace and reconstructs its index; copying an old database into `.rag-go` is not a migration.

1. **Before replacing the old binary**, use v0.1 to stop and uninstall its user service:

   ```sh
   rag service uninstall
   ```

   If you used only a foreground server, stop it. If the v0.1 binary has already been replaced, temporarily recover the previous release to uninstall its own service. Do not delete the global store or modify unrelated LaunchAgents/systemd units.

2. Install/build v0.2. Legacy `ragd`, `ragctl`, `ragprep`, `rageval`, service commands, path registration and PDF-backend options are removed. Script upgrades may leave old executables in PATH; remove those deliberately after stopping their old service.

3. Prepare a single documents root. Reuse existing canonical packages containing `rag-source.json`, TEI/JATS, normalized blocks JSON or recognizable MinerU exports. You can copy complete packages from the old `prepared/` cache, including their manifest and canonical content, without copying `conversion.json`. The original global store remains untouched. If documents are still PDFs, convert them with an external tool first.

4. Initialize the workspace and rebuild:

   ```sh
   cd /absolute/my-project
   rag init --docs /absolute/converted-papers
   rag sync
   rag status
   ```

   Each workspace has its own provider configuration, credentials and `.rag-go` index. Existing environment credentials can be reused. Do not paste secrets into project configuration or registration files.

5. Reconnect Agents:

   ```sh
   rag connect claude --replace
   rag connect codex --replace
   ```

   Claude receives a local project registration; Codex receives a project configuration override. Registrations use the absolute v0.2 binary and workspace. Legacy user-level HTTP registrations may remain outside this project; remove them deliberately through the respective client if no other workspace uses them. Reload the Agent and call `rag_status`, then query a known passage.

6. Confirm document counts, titles, provenance and known query results before deciding whether to archive old stores. Unknown pages stay unknown. A local test/registration is separate from actual model-provider and Agent acceptance.

| v0.1 | v0.2 |
| --- | --- |
| Global store / `--store` | Nearest workspace / `--workspace` |
| `add`, `index`, tracked paths | Single configured documents root and `sync` |
| `refresh` / watcher | `sync` or query-triggered synchronization |
| `remove` | Remove a document from its source root, then sync |
| `serve`, `stdio`, user service | `mcp` stdio; optional foreground read-only HTTP |
| `prep`, automatic PDF conversion | External document extraction |
| `cleanup` | `clean`, preview by default, deletion with `--confirm` |
| `clear` | Initialize another workspace when a separate empty knowledge base is needed |
| `rageval` | `rag eval`, directly against Core |
| Legacy TypeScript read mode | Use the old version for old-store access; rebuild a new workspace |

After changing embedding, chunking or documents-root configuration, run `rag rebuild`. Its staging/atomic publication retains the previous active database if rebuilding fails. `rag clean` deletes recognized inactive generations only, never source documents or the active index.
