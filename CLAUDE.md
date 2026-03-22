# RISE RISC-V Runners Documentation

Jekyll site using the **just-the-docs** theme, deployed to GitHub Pages at `riscv-runners.riseproject.dev`.

## Local Development

```sh
bundle install
bundle exec jekyll serve
```

Site available at `http://localhost:4000`.

## Adding a Page

Create a `.md` file in the appropriate `docs/` subdirectory with front matter:

```yaml
---
title: Page Title
parent: Parent Section Title
nav_order: 1
---
```

Pages appear in the navigation automatically.

## Editing Existing Pages

All content lives in `docs/` and `index.md`. Use standard Markdown with Mermaid fenced blocks for diagrams.

## Navigation

Controlled by `nav_order` in front matter. Parent pages use `has_children: true`.

## Diagrams

Use Mermaid fenced code blocks (` ```mermaid `). Rendered client-side by just-the-docs.

## Deployment

Automatic on push to `main` via `.github/workflows/pages.yml`. No manual deploy needed.

## Related Repos

- [`riscv-runner-app`](https://github.com/riseproject-dev/riscv-runner-app): GitHub App webhook handler and worker
- [`riscv-runner-device-plugin`](https://github.com/riseproject-dev/riscv-runner-device-plugin): Kubernetes device plugin and node labeller
- [`riscv-runner-images`](https://github.com/riseproject-dev/riscv-runner-images): Runner container images
- [`riscv-runner-sample`](https://github.com/riseproject-dev/riscv-runner-sample): Sample repository demonstrating usage

Architecture pages reference source files in these repos.

## Style

Target audience: software engineers.

### Voice

Impersonal/third person for the service ("the service provisions runners", "runners are ephemeral"). Use "we" only when referring to RISE as the organization maintaining or funding the service ("RISE provides this service free of charge").

### Tone by page type

- **Home page** (`index.md`): Engineer-to-engineer. Factual, dry, but reads like one engineer explaining to another.
- **All other pages** (getting-started, architecture, FAQ): Reference-like. Neutral, documentation-style.

### Sentence structure

Natural but concise. Normal prose, trimmed of fat. Not telegraphic, not verbose.

### Banned patterns

- **No superlatives:** "best", "fastest", "most powerful", "seamlessly", "effortlessly", etc.
- **No em dashes (—):** Rewrite naturally case-by-case. Use periods, colons, commas, or parentheses instead.
- **No corporate filler:** "leverage", "robust", "ecosystem", "paving the way", "game-changer", "cutting-edge", "innovative", etc.
- **No vague modifiers:** "easily", "simply", "just", "seamlessly", "effortlessly", "quickly", etc.

### Allowed

- **Hedging** when technically accurate ("aims to" is honest when something is in progress).
- **Honest constraints.** State limitations directly. Don't gloss over them.
- **Direct statements.** Say what it does, not what it "helps" do.
