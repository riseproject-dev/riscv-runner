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

- [`riscv-runner-app`](https://github.com/riseproject-dev/riscv-runner-app) — GitHub App webhook handler and worker
- [`riscv-runner-device-plugin`](https://github.com/riseproject-dev/riscv-runner-device-plugin) — Kubernetes device plugin and node labeller
- [`riscv-runner-images`](https://github.com/riseproject-dev/riscv-runner-images) — Runner container images
- [`riscv-runner-sample`](https://github.com/riseproject-dev/riscv-runner-sample) — Sample repository demonstrating usage

Architecture pages reference source files in these repos.

## Style

Technical, terse, no buzzwords. Target audience is software engineers.
