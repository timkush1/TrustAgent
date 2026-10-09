// Package version holds the build version of the proxy.
// Keep in sync with the root VERSION file; overridable at build time:
//
//	go build -ldflags "-X github.com/truthtable/backend-go/internal/version.Version=x.y.z"
package version

var Version = "1.0.0"

// GitSHA is the git revision the binary was built from, set at build time by the Docker build
// (the build context has no .git, so Go's own VCS stamp is empty there):
//
//	go build -ldflags "-X github.com/truthtable/backend-go/internal/version.GitSHA=$(git rev-parse --short HEAD)"
//
// Empty when the build did not set it.
var GitSHA = ""
