# GoDeploy

GoDeploy is a desktop app for managing Git-backed Node.js projects and running a local deployment pipeline.

> **Current scope:** Deploy updates a project's Git checkout, chooses Bun when a Bun lockfile is present (otherwise npm), builds the project, and starts it with PM2. Next.js standalone builds are staged as self-contained runtime directories.

## Screenshot

![GoDeploy project workspace](docs/sample-1.png)

## Features

- Create, edit, search, and delete projects.
- Configure a repository URL, branch, optional personal access token, Git identity, optional build command, and environment variables from a file.
- View detected `package.json` metadata, framework type, and sorted script names/commands.
- Follow deployment output and status in the app.
- Clone private HTTPS repositories with a personal access token. The token is masked in project details, redacted from Git output, and removed from the checkout's `origin` URL after cloning.

## Deploy workflow

The **Deploy** button runs these steps in order and stops if a step fails:

1. Clone the selected branch into GoDeploy's data directory, or fetch and pull the selected branch when the checkout already exists.
2. If `ENV` selects a profile and `.env.<ENV>` exists, load that profile, then apply configured environment variables and write `.env` beside the detected `package.json`.
3. Use `bun install --frozen-lockfile` when `bun.lock` or `bun.lockb` exists. Otherwise run `npm install`, retrying once with `--legacy-peer-deps` if needed.
4. Run the configured build command. On Windows, semicolon-separated custom commands are executed sequentially. Otherwise, prefer a Next.js `prod-build` script, then the package's `build` script. Require the framework's expected output directory. Next.js builds receive `NODE_OPTIONS=--max_old_space_size=8192` and `RAYON_NUM_THREADS=4`.
5. Detect Next.js standalone mode from `output: "standalone"` in `next.config.*` and Nuxt's Node server mode from `nitro.preset: "node-server"` in `nuxt.config.*`. Stage Next.js `.next/standalone` plus `public` and `.next/static`, or Nuxt `.output`, under `dist/<project-name>` with runtime environment files and a PM2 ecosystem config. If the config requires standalone output but the build artifact is missing, stop with an error.
6. Start staged Next.js and Nuxt standalone builds with the generated PM2 ecosystem config. On Windows, non-standalone Next.js apps with a standard `next start` script run the Next.js CLI directly to avoid npm spawning visible command windows. Other projects use `npm start`. Existing stopped entries are replaced when their launch command must change.

Git, npm, build, and PM2 output is shown in the deployment terminal. Deploy is disabled while the matching PM2 process is running.

## Requirements

- Go 1.25.5 or later to run from source.
- Git available on `PATH`.
- Node.js with npm available on `PATH`; Bun is required when the project has a Bun lockfile.
- PM2 available on `PATH` to read the process list and start or restart projects.

## Run from source

The UI and shared function code are separate Go modules. From the repository root:

```sh
cd ui
go run .
```

## Tests

Run tests from each module directory:

```sh
cd function
go test ./...
```

```sh
cd ui
go test ./...
```

## Project data

GoDeploy creates its configuration file on first launch:

- Windows: `%APPDATA%\GoDeploy\config.json` (or `%LOCALAPPDATA%` if `%APPDATA%` is unavailable)
- macOS: `~/Library/Application Support/GoDeploy/config.json`
- Linux and other Unix-like systems: `$XDG_CONFIG_HOME/GoDeploy/config.json`, or `~/.config/GoDeploy/config.json` when `XDG_CONFIG_HOME` is unset

Project checkouts are stored alongside this configuration file; staged Next.js standalone deployments are stored under `dist/<project-name>` in the same data directory. Personal access tokens are stored in the configuration file, so protect it like a secret.
