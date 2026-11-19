# GoDeploy

GoDeploy is a desktop app for managing Git-backed Node.js projects and running a local npm deployment pipeline.

> **Current scope:** The Deploy workflow clones a repository, installs dependencies, builds it, and starts it with PM2 using `npm start`. It does not run Bun commands.

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

1. Clone the selected branch into GoDeploy's data directory. If the project directory already exists, it is reused; GoDeploy does not pull updates.
2. If environment variables are configured, write them to `.env` beside the detected `package.json`.
3. Run `npm install`. If it fails, retry once with `--legacy-peer-deps`.
4. Run the configured build command, or use `npm run build` when `package.json` defines a `build` script. Require the framework's expected output directory; skip this step if no command is configured.
5. Start the project with PM2 using `npm start`. If a matching stopped PM2 process already exists, restart it instead.

Git, npm, build, and PM2 output is shown in the deployment terminal. Deploy is disabled while the matching PM2 process is running.

## Requirements

- Go 1.25.5 or later to run from source.
- Git available on `PATH`.
- Node.js with npm available on `PATH`.
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

Project checkouts are stored alongside this configuration file. Personal access tokens are stored in the configuration file, so protect it like a secret.
