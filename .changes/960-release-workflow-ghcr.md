## Release workflow: the Docker Hub job is removed; GHCR is the image home

**Symptom.** Every release run failed its `docker` job at "Log in to
Docker Hub" (no valid DOCKERHUB secrets), reddening otherwise-good
releases.

**Root cause.** The job was an upstream leftover twice over: it pushed
to `luqmenul/9router-go` — the UPSTREAM author's Docker Hub
repository, a coordinate this fork neither owns nor may push to — and
this fork already publishes its real image to GHCR
(ghcr.io/rmz17x/axonrouter-go) from the Publish Docker Image workflow,
which succeeded for v1.9.16 on the same tag push.

**Fix.** The Docker Hub job is deleted from release.yml, along with
the channel job's now-dead docker_tags output. The Telegram notify job
needs only [binaries] (its comment records that the image's own
workflow run is its success signal). REGISTRY_IMAGE now names the GHCR
coordinate, and the prerelease banner's pull line uses it with the
full tag (GHCR tags keep the `v`). One trade-off, recorded: the
removed job built linux/arm64 as well as amd64; the GHCR workflow
builds amd64 — arm64 users are served by the native release binaries.
