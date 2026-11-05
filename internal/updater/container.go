package updater

import (
	"errors"
	"os"
	"strings"
)

// ContainerMarkerEnv is the environment marker the Docker image and both
// compose stacks set on the gateway process (AXONROUTER_IN_CONTAINER=1).
// It is how the binary knows it is running as a container deployment —
// the one shape this product officially supports — as opposed to a bare
// binary an operator runs themselves (unsupported DIY).
const ContainerMarkerEnv = "AXONROUTER_IN_CONTAINER"

// ErrSelfUpdateDisabledInContainer is returned by CheckUpdate and
// PerformSelfUpdate when the container marker is set. A container's
// binary is part of its image; replacing it at runtime would be undone
// by the next recreate and would desynchronise the node from its stack,
// so the whole self-update surface refuses with this explanation and
// updates ship the way every other container change does: a new image.
var ErrSelfUpdateDisabledInContainer = errors.New("self-update is disabled in a container deployment: updates are delivered as new container images — pull the updated image and recreate the container")

// InContainer reports whether the container marker is set. The image and
// the compose stacks set it to "1"; "true" is accepted as well so an
// operator-supplied value in either common spelling means the same thing.
func InContainer() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(ContainerMarkerEnv))) {
	case "1", "true":
		return true
	}
	return false
}
