Novel2Script Agent local package

1. Run Start-Novel2ScriptAgent.ps1 once. It creates .env and asks you to add the model API key.
2. Add N2S_LLM_API_KEY to .env, then run Start-Novel2ScriptAgent.ps1 again.
3. Use Stop-Novel2ScriptAgent.ps1 before backup, restore, or moving the package.
4. Data is stored in data/. Do not edit the database files manually.
5. tools/backup-data.ps1 creates a backup without API keys or model trace logs.
6. tools/restore-data.ps1 restores a backup after explicit confirmation.
7. tools/view-logs.ps1 shows metadata-only model call diagnostics.
