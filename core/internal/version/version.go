// Package version carries the build stamp, set via -ldflags at image build time.
package version

// Version is overwritten by the Dockerfile's core-build stage.
var Version = "0.0.0-dev"
