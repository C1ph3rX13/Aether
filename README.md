<h1 align="center">Aether</h1>

<p align="center">A penetration testing AGENT built on intent engineering and first principles.</p>

<p align="center">
  <img src="https://img.shields.io/badge/Aether-0.1.0-2563EB" alt="Aether 0.1.0" />
  <img src="https://img.shields.io/badge/Go-1.26.4-00ADD8?logo=go&amp;logoColor=white" alt="Go 1.26.4" />
  <img src="https://img.shields.io/badge/Next.js-16.3.6-000000?logo=nextdotjs&amp;logoColor=white" alt="Next.js 16.3.6" />
  <img src="https://img.shields.io/badge/Workers-Docker-2496ED?logo=docker&amp;logoColor=white" alt="Docker Workers" />
</p>

<p align="center">
  <a href="README.md">English</a> ·
  <a href="README_CN.md">简体中文</a> ·
  <a href="INSTALL.md">Installation &amp; deployment</a>
</p>

![Aether Projects dashboard](Aether.png)

## Contents

- [Features](#features)
- [Quick start](#quick-start)
- [Runtime modes](#runtime-modes)
- [Source layout](#source-layout)
- [Development checks](#development-checks)
- [Acknowledgements](#acknowledgements)

## Features

- **Worker runtimes:** Claude Code, Codex, and Pi, each with its own provider settings and credentials.
- **Execution history:** project state and execution records persisted in SQLite.
- **Runtime settings:** configurable concurrency, task deadlines, retries, prompt groups, and project containers.
- **Operator controls:** pause, resume, complete with evidence, reopen, replay, and export project snapshots.
- **Versioned configuration:** new projects use saved settings; existing projects retain their configuration revision.
- **Native deployment:** a Next.js interface embedded in a Go binary, with combined or separate Server and Dispatcher modes.

## Quick start

Build requirements: Go 1.26.4 or newer, Node.js 20.9 or newer, npm, Bash, and Git. Running Workers also requires an accessible Docker daemon, a compatible Worker image, and model provider credentials. Linux is the validated deployment environment.

```bash
git clone https://github.com/C1ph3rX13/Aether.git
cd Aether
./install.sh
export PATH="$HOME/.local/bin:$PATH"
aether --version
docker pull ghcr.io/c1ph3rx13/aether-worker:latest
```

The installer builds the frontend and backend, then installs `~/.local/bin/aether`. To select another writable destination, use `./install.sh /path/to/bin`. The current application version is **0.1.0**.

Create your deployment manifest using the [first launch guide](INSTALL.md#first-launch), then start Aether from the repository root:

```bash
aether run --deployment "$PWD/configs/bootstrap-deployment.toml"
```

> [!TIP]
> **Recommended: use `aether run`.** It starts the Server and Dispatcher together in one process.

Open [http://127.0.0.1:8000](http://127.0.0.1:8000), [configure your first Worker](INSTALL.md#configure-the-first-worker), and create a project from **Projects → New Project**.

## Runtime modes

All modes require an explicit TOML deployment manifest, based on [the included example](configs/bootstrap-deployment.example.toml).

| Command | Responsibility |
| --- | --- |
| `aether run -d deployment.toml` | **Recommended.** Server and Dispatcher together in one process. |
| `aether serve -d deployment.toml` | Browser interface, HTTP API, and SQLite; no Docker access. |
| `aether dispatch -d deployment.toml` | Scheduling and Docker Worker execution; communicates with the Server over HTTP. |

The manifest contains deployment identity, listen address, database path, and Dispatcher connection settings. Worker credentials, models, container settings, and scheduling policies are configured in the web interface and persisted in SQLite.

## Source layout

| Path | Contents |
| --- | --- |
| `cmd/aether/` | Native CLI and runtime composition. |
| `internal/` | Control plane, configuration, project state, scheduling, Worker adapters, and database migrations. |
| `internal/worker/prompts/` | Bundled runtime prompts. |
| `web/` | Next.js frontend and component tests. |
| `configs/` | Deployment manifest example. |
| `test/` | Acceptance, end-to-end, and browser scenario code. |
| `install.sh` | Source build and native installation. |

## Development checks

```bash
go test ./cmd/... ./internal/... ./test/...
go vet ./cmd/... ./internal/... ./test/...

cd web
npm ci
npm run build
npm run lint
npm run typecheck
npm test
```

For race checks, manual builds, and tests that use real Docker containers, see [INSTALL.md](INSTALL.md#verification).

## Acknowledgements

Thanks to [Cairn](https://github.com/oritera/Cairn), its maintainers, and its contributors for sharing their work with the community.
