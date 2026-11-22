1. project.go::FetchRepositoryWithOutput
2. npm_package.go::NpmInstallWithOutput
3. npm_package.go::NpmBuildWithOutput (run if scripts.build exists; require the framework's expected output directory)
4. proccess_manager.go::Pm2DeployWithOutput (write ecosystem.config.js beside package.json and start the project from that PM2 configuration)
