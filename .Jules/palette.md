## 2025-02-18 - [CLI Loading States]
**Learning:** Users need visual feedback during opaque long-running operations (like code generation or dependency updates) to confirm the tool is working.
**Action:** Use `ui.RunSpinner` to wrap any CLI operation that takes more than 100ms or involves external process execution.

## 2025-02-18 - [CLI Error Messages]
**Learning:** Interactive prompts can be disruptive. Users often prefer clear, styled error messages over unexpected interactivity.
**Action:** Use `ui.PrintError` for missing arguments instead of prompting or using raw `cobra` errors.
