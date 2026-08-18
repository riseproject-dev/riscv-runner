# Project conventions

## Comments

Default: no comment. Names and structure should carry the meaning.

When a comment is warranted, explain the *why*, not the *what*. Good targets:

- A non-obvious constraint or invariant.
- A workaround for a known bug (link the issue if you have one).
- A safety property the reader could miss from the code alone.

Avoid:

- Restating the function name in English.
- Multi-paragraph blocks. Two short lines is usually enough; four is the cap.
- Narrating control flow the reader can already see.
- References to the current task, PR, or caller. Those rot.

If you cannot say it in a couple of lines, rename or restructure instead.

## Style

- Run `go vet ./...`, `gofmt -l .`, `go test -race ./...` in any module you touch (`control-plane/`, `device-plugin/`) before considering work done.
- No em dashes anywhere (rewrite naturally with periods, colons, commas, or parentheses).
- Component-specific docs live in each subdirectory's `README.md`. The exhaustive reference is on the [website](https://riscv-runners.riseproject.dev/); writing style guide for the site is in [`website/CLAUDE.md`](website/CLAUDE.md).
