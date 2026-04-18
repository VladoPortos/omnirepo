## Playwright webServer shell syntax (discovered 07-03)

`web/playwright.config.ts` `webServer.command` uses bash subshell syntax
`(cd web && ...)` but `/bin/sh` on this env does not support it — any
`npx playwright test <spec>` run fails with:

```
/bin/sh: 1: Syntax error: "(" unexpected
Error: Process from config.webServer was not able to start. Exit code: 2
```

Reproduces on existing specs (`error-envelope.spec.ts`), so this is not
new with 07-03. Fix: wrap the command in `bash -c '...'` or use `&&` with
`cd ...; ...` and remove the subshell. Out-of-scope for 07-03 (spec wiring);
file a separate micro-fix in a later 07-* plan.

This does NOT block 07-03 because the acceptance criterion for the full
run is conditional ("When the dev server is running"), and the spec
itself passes the mandatory `--list` parse check.
