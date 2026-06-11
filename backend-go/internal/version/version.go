// Package version holds the build version of the proxy.
// Keep in sync with the root VERSION file; overridable at build time:
//
//	go build -ldflags "-X github.com/truthtable/backend-go/internal/version.Version=x.y.z"
package version

var Version = "1.0.0"
