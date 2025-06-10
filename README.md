# BitBot Project

A Discord bot with various functionalities, including voice interaction using AI.

## Local Development and Build Dependencies

To build and run this bot locally, you'll need Go and some system dependencies, especially for the voice features.

### Go Version

*   Go 1.21 or newer is recommended. Please refer to the `go.mod` file for the specific version used in this project.

### System Dependencies for Voice Features

The voice features rely on libraries that require CGo and have system-level dependencies.

#### Debian/Ubuntu-based Systems

Install the following packages:
`sudo apt-get update && sudo apt-get install libopus-dev libsoxr-dev pkg-config`

Runtime libraries (`libopus0`, `libsoxr0`) are typically installed as dependencies of the `-dev` packages.

#### Alpine Linux

For reference (as used in the Dockerfile), the equivalent packages are:
*   Build-time: `apk add --no-cache gcc musl-dev opus-dev sox-dev pkgconfig`
*   Run-time: `apk add --no-cache opus sox`

#### Build Command

You may need to build with CGo enabled:
`CGO_ENABLED=1 go build .`
