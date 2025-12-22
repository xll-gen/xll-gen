## 2025-02-18 - [CLI Loading States]
**Learning:** Users need visual feedback during opaque long-running operations (like code generation or dependency updates) to confirm the tool is working.
**Action:** Use `ui.RunSpinner` to wrap any CLI operation that takes more than 100ms or involves external process execution.

## 2025-02-18 - [CLI Interactive Prompts]
**Learning:** For `init` commands, prompting for missing arguments is friendlier than erroring out.
**Action:** Use `ui.Prompt` when optional arguments are missing in CLI commands.
