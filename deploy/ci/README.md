# CI workflows

`ci.yml` and `release.yml` belong at `.github/workflows/`. They are kept here instead because the
credential this repository is pushed with is a GitHub App without the `workflows` permission, and
GitHub rejects *any* push that creates or modifies a file under `.github/workflows` from such a
credential — including a push that only meant to change something else.

To install them:

```sh
mkdir -p .github/workflows
cp deploy/ci/ci.yml deploy/ci/release.yml .github/workflows/
git add -f .github/workflows
git commit -m "Add CI workflows"
git push        # needs a credential with the `workflows` scope
```

A personal access token with `repo` + `workflow`, or granting the App that permission, will do it.

## What they do

**`ci.yml`** — on every push to `main` and every pull request. Runs `make gates`: gofmt, `go vet`,
golangci-lint and `go test -race`, all inside the same container stages the release image is built
from, so CI is not a second environment that can drift from what a developer sees. A second job
builds the production image, so a broken Dockerfile and a broken test are distinguishable at a
glance.

**`release.yml`** — on a `v*` tag. Runs the gates again on the exact commit being published, then
builds and pushes to `ghcr.io/<owner>/springback` for `linux/amd64` and `linux/arm64`. The version
baked into the binary comes from the tag, so `springback version` and `/api/health` report
something that corresponds to a commit.

`v0.11.0` publishes `:0.11.0`, `:0.11` and `:latest`.

**One thing to watch on the first release:** the arm64 image is built under QEMU emulation, and
that stage compiles ipatool and ideviceinstaller from source. Expect it to be slow — tens of
minutes — and if it turns out to be unreliable, dropping `linux/arm64` from `platforms` costs
nothing but ARM users.
