<h1 align="center">Installation &amp; deployment</h1>

<p align="center">Build Aether from source, prepare Docker, and start your first Project.</p>

<p align="center">
  <a href="INSTALL.md">English</a> ·
  <a href="INSTALL_CN.md">简体中文</a> ·
  <a href="README.md">Back to README</a>
</p>

> [!TIP]
> **Recommended: start Aether with `aether run`.** It starts the Server and Dispatcher together. These instructions target Linux; see [separate deployment](#separate-server-and-dispatcher) when the two roles need different hosts or permissions.

## Contents

- [Requirements](#requirements)
- [Build and install](#build-and-install)
- [Prepare the Worker image](#prepare-the-worker-image)
- [First launch](#first-launch)
- [Configure the first Worker](#configure-the-first-worker)
- [Separate Server and Dispatcher](#separate-server-and-dispatcher)
- [Health and persistence](#health-and-persistence)
- [Verification](#verification)
- [Troubleshooting](#troubleshooting)

## Requirements

| Requirement | When needed | Details |
| --- | --- | --- |
| Go | Building | 1.26.4 or newer, as declared in `go.mod`. |
| Node.js and npm | Building | Node.js 20.9 or newer; the build was verified with Node.js 24. |
| Bash, Git, and Unix tools | Installing | Includes `install` and `mktemp`. |
| Docker daemon and Worker image | Executing tasks | The Dispatcher must be able to access the daemon and the selected image. |
| Model provider credentials | Executing tasks | Configured separately for each Worker. |

Building requires access to Go modules, npm packages, and Google Fonts. Worker execution requires access to the configured provider. The installed binary includes the frontend, so Node.js is not required on the application host after building.

## Build and install

```bash
git clone https://github.com/C1ph3rX13/Aether.git
cd Aether
./install.sh
export PATH="$HOME/.local/bin:$PATH"
aether --version
```

`aether --version` should report **0.1.0**. Add the `PATH` line to your shell configuration to keep it across sessions. The default destination is `~/.local/bin/aether`; to use another writable directory:

```bash
./install.sh /path/to/bin
```

The script runs `npm ci`, produces a static frontend, embeds it into a Go binary, and writes `dist/aether` before installing it. A failed build preserves an existing installed binary. It does not install prerequisites, configure Docker, create a deployment, or register a system service.

## Prepare the Worker image

Run these commands on the host whose Docker daemon the Dispatcher will use:

```bash
docker info
docker pull ghcr.io/c1ph3rx13/aether-worker:latest
docker image inspect ghcr.io/c1ph3rx13/aether-worker:latest
```

The image must be available on that daemon before starting project execution; Aether does not automatically pull it. The recommended configuration uses this image and `host` networking. A custom image must support the selected Claude Code, Codex, or Pi runtime. This repository contains the application source, not a Worker image build definition.

## First launch

For a new deployment, run the following from the repository root to create a private data directory and a local manifest:

```bash
mkdir -p data
chmod 700 data
cat > configs/bootstrap-deployment.toml <<EOF
schema = "aether.bootstrap/v1"

[deployment]
id = "aether-local"

[operator]
name = "local-operator"

[server]
listen_host = "127.0.0.1"
listen_port = 8000
database_path = "$PWD/data/aether.db"

[dispatcher]
control_plane_url = "http://127.0.0.1:8000/api/v1"
docker_host = "unix:///var/run/docker.sock"
EOF
```

Start Aether with the recommended command:

```bash
aether run --deployment "$PWD/configs/bootstrap-deployment.toml"
```

Open **[http://127.0.0.1:8000](http://127.0.0.1:8000)**. The command stays in the foreground; use Ctrl+C to stop it.

The shell expands `$PWD` when writing this example. Aether itself does not expand environment variables or `~` in manifest values: `database_path` must be an absolute path. If your Docker socket is elsewhere, set its actual absolute path in `docker_host`; `DOCKER_HOST` is not used as a fallback.

Use a stable deployment ID for a database, and different IDs for independent deployments sharing a Docker daemon. Keep the service on a trusted interface. The application API has no built-in login; remote access should go through an authenticated access layer.

`data/` and `configs/bootstrap-deployment.toml` are ignored by Git. Keep Worker settings and API keys out of the manifest; only the fields in [the example](configs/bootstrap-deployment.example.toml) are supported.

## Configure the first Worker

> [!IMPORTANT]
> A fresh database starts without active configuration. The interface is available, but execution waits for configuration.

1. Open **Workers**, select Claude Code, Codex, or Pi, and choose **New Worker**.
2. Enter a unique name, a positive capacity, and supported task types. For a simple initial setup, enable **Bootstrap**, **Reason**, and **Explore** on one Worker. Across all Workers, these three task types must be covered before creating Projects.
3. Continue to **Provider**, choose the provider configuration, and enter the endpoint, model, and credential requested by the form.
4. Review and **Save Worker**. Saving checks the provider; correct any validation or connection errors before proceeding.
5. Review **Runtime**, **Tasks**, and **Prompts**. In **Runtime → Project**, verify the Worker image and network mode match your Docker environment. Recommended values are available in the settings pages.
6. Wait for **Project creation ready**, then open **Projects → New Project** and enter the title, Origin, and Goal.

Use **Origin** for the known starting context and **Goal** for the desired outcome. Project creation requires saved configuration with Bootstrap, Reason, and Explore coverage. If the Dispatcher is unavailable, projects can still be created; execution waits until it returns.

| Adapter | Main provider fields | Credential |
| --- | --- | --- |
| Claude Code | `ANTHROPIC_BASE_URL`, `ANTHROPIC_MODEL`, `IS_SANDBOX` | `ANTHROPIC_AUTH_TOKEN` |
| Codex | `CODEX_BASE_URL` supporting the Responses API, `CODEX_MODEL` | `OPENAI_API_KEY` |
| Pi | `PI_BASE_URL`, `PI_MODEL`, `PI_PROVIDER_API` | `PI_API_KEY` |

Pi supports `anthropic-messages`, `openai-responses`, and `openai-completions` provider API modes. Configure these fields through the Worker editor. Credentials are persisted with deployment data and hidden in subsequent reads; protect the database and its backups.

Saving configuration makes it effective for new Projects. Existing Projects retain the configuration revision assigned at creation; changing a Worker or runtime setting does not update those Projects.

## Separate Server and Dispatcher

Start the Server first:

```bash
aether serve --deployment /absolute/path/deployment.toml
```

In another terminal or service, start the Dispatcher:

```bash
aether dispatch --deployment /absolute/path/deployment.toml
```

The Server owns SQLite and does not need Docker permissions. The Dispatcher owns scheduling and Docker access, and connects to `dispatcher.control_plane_url`, including `/api/v1`. When running on another host, give the Dispatcher a manifest with the same deployment ID and the reachable Server URL. It does not open the Server's database path.

`aether run` combines both roles and needs both database and Docker access. Run the selected commands under your service manager for persistent deployment; the installer does not create service units.

## Health and persistence

```bash
curl -i http://127.0.0.1:8000/api/v1/health
curl -i http://127.0.0.1:8000/api/v1/ready
```

| Endpoint | HTTP response | Meaning |
| --- | --- | --- |
| `/health` | `200`, `status: ok` | The HTTP service responds and the database is reachable. |
| `/ready` | `200`, `status: ready` | Active configuration and an active Dispatcher are available. |
| `/ready` | `503`, `configuration_required` | Save the deployment configuration. |
| `/ready` | `503`, `dispatcher_unavailable` | Start or restore the Dispatcher. |
| Either | `503`, `database_unavailable` | Check database availability and the Server logs. |

Readiness does not check every provider or guarantee that a Worker invocation will succeed. Use the Worker editor and execution records to investigate provider or runtime failures.

SQLite contains project state, configuration revisions, and credential data. For a simple offline backup, stop the Dispatcher and Server cleanly, then copy the complete data directory and deployment manifest into protected storage. Files in project containers require a separate backup; a database copy does not include container filesystems. Keep backups outside the source repository.

## Verification

Run backend checks from the repository root:

```bash
go test ./cmd/... ./internal/... ./test/...
go vet ./cmd/... ./internal/... ./test/...
go test -race -timeout=60m ./cmd/... ./internal/... ./test/...
```

SQLite-heavy race tests can take tens of minutes per package. The timeout above applies to each package. Frontend checks:

```bash
cd web
npm ci
npm run build
npm run lint
npm run typecheck
npm test
```

Real Docker integration tests create containers and require the Worker image above. From the repository root:

```bash
AETHER_REQUIRE_DOCKER_TESTS=1 \
AETHER_DOCKER_TEST_IMAGE=ghcr.io/c1ph3rx13/aether-worker:latest \
go test -p 1 -tags dockerintegration ./internal/docker ./cmd/aether \
  -run DockerIntegration -count=1 -timeout=15m
```

For a manual native build, generate the frontend first:

```bash
(cd web && npm ci && npm run build)
mkdir -p dist
CGO_ENABLED=0 go build -buildvcs=false -trimpath -tags embedstatic \
  -o dist/aether ./cmd/aether
./dist/aether --version
```

The `embedstatic` build tag is required to include the production interface.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Frontend build cannot download dependencies or fonts | Network access to the configured registries and Google Fonts. |
| `aether` is not found | Installation directory is in `PATH`, or invoke the binary by its absolute path. |
| Manifest validation fails | Explicit manifest path, supported TOML fields, absolute database/socket paths. |
| Docker permission or connection error | Daemon availability and the executing user's access to the configured socket. |
| Worker image is missing | Pull the configured image on the Dispatcher daemon. |
| Provider check fails on Save | Endpoint, API protocol, model, credential, and network reachability. |
| Project creation is unavailable | Saved configuration and Bootstrap/Reason/Explore coverage. |
| Projects wait without executing | Dispatcher availability, Worker image, provider connectivity, and configured capacity. |
| Deployment identity mismatch | The manifest ID must match the identity already persisted in the database. |

To remove the installed application, delete its installed binary. Deployment databases and Docker project data are managed separately.
