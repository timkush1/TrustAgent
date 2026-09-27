# v2 baseline test results (commit 5b6f111, 2026-09-27)

Recorded before any v2 change, on WSL2 with Go 1.25 toolchain, Python 3.12 poetry venv, Node 22.

```
== python
........................................................................ [ 84%]
.............                                                            [100%]
85 passed, 12 deselected in 7.39s
== go
?   	github.com/truthtable/backend-go/api/audit/v1	[no test files]
ok  	github.com/truthtable/backend-go/cmd/proxy	0.015s
?   	github.com/truthtable/backend-go/internal/config	[no test files]
?   	github.com/truthtable/backend-go/internal/grpc	[no test files]
?   	github.com/truthtable/backend-go/internal/metrics	[no test files]
ok  	github.com/truthtable/backend-go/internal/middleware	0.007s
ok  	github.com/truthtable/backend-go/internal/proxy	0.058s
ok  	github.com/truthtable/backend-go/internal/store	0.003s
?   	github.com/truthtable/backend-go/internal/version	[no test files]
ok  	github.com/truthtable/backend-go/internal/websocket	0.006s
ok  	github.com/truthtable/backend-go/internal/worker	0.156s
== react
> frontend-react@1.0.0 test
> vitest run

8:41:24 PM [vite] warning: `esbuild` option was specified by "vite:react-babel" plugin. This option is deprecated, please use `oxc` instead.
8:41:24 PM [vite] warning: `optimizeDeps.esbuildOptions` option was specified by "vite:react-babel" plugin. This option is deprecated, please use `optimizeDeps.rolldownOptions` instead.
Both esbuild and oxc options were set. oxc options will be used and esbuild options will be ignored. The following esbuild options were set: `{ jsx: 'automatic', jsxImportSource: undefined }`

 RUN  v4.1.8 /home/timkushmar/projects/TrustAgent/frontend-react


 Test Files  4 passed (4)
      Tests  30 passed (30)
   Start at  20:41:24
   Duration  2.06s (transform 339ms, setup 0ms, import 1.03s, tests 467ms, environment 5.24s)

== golden
  accuracy            0.9000
  balanced_accuracy   0.8990
  auroc               0.9071
  ece                 0.1325

  Confusion: TP=24 FP=3 TN=21 FN=2

Baseline check passed (golden_v1.json).
```
