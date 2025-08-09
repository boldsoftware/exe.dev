- when changing code, always run `go test ./...` and fix any errors
- this git repository implements the exe.dev service
- exe.dev is a service users can use to start containers with persistent disks
- containers are spun up on GKE autopilot + PVC
- the exed server is both the web frontend and ssh frontend
- users can `ssh exe.dev` and get a guided console management tool
- after enough time without an ssh connection or a web request, containers are
  paused. they are reinstated on incoming HTTP request or ssh connection
