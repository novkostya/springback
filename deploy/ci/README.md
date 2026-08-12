# CI workflows

`ci.yml`, `release.yml` and `demo-deploy.yml` belong at `.github/workflows/`. They are kept here instead because the
credential this repository is pushed with is a GitHub App without the `workflows` permission, and
GitHub rejects *any* push that creates or modifies a file under `.github/workflows` from such a
credential — including a push that only meant to change something else.

To install them:

```sh
mkdir -p .github/workflows
cp deploy/ci/ci.yml deploy/ci/release.yml deploy/ci/demo-deploy.yml .github/workflows/
git add -f .github/workflows
git commit -m "Add CI workflows"
git push        # needs a credential with the `workflows` scope
```

A personal access token with `repo` + `workflow`, or granting the App that permission, will do it.

## The action versions are checked, not remembered

Both files pin every action to a major, and those pins were wrong on the first attempt: written
from memory they came out as `actions/checkout@v4` and each `docker/*` action one major behind,
all targeting a Node 20 runtime GitHub has since deprecated. There was no reason for it beyond v4
being what most workflow YAML in the world still says.

[`.github/dependabot.yml`](../../.github/dependabot.yml) now watches them weekly and groups the
updates into one pull request. To check by hand:

```sh
# Reads the action names out of the workflows, so it cannot go stale when one is added.
for r in $(grep -rhoE 'uses: [^@]+' .github/workflows deploy/ci | sed 's/uses: //' | sort -u); do
  echo -n "$r: "
  git ls-remote --tags --refs https://github.com/$r |
    sed 's#.*refs/tags/##' | grep -E '^v?[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1
done
```

The current majors were read against their release notes before being bumped. The only
behavioural change that touches this repo is checkout's: v5 and later refuse to check out a fork's
head for `pull_request_target` and `workflow_run` unless `allow-unsafe-pr-checkout` is set.
`ci.yml` uses plain `pull_request`, so it is unaffected — and if that ever changes, the safe
default is the one to keep. Everything else in the bump is Node 24 and ESM.

## What they do

**`ci.yml`** — on every push to `main` and every pull request. Runs `make gates`: gofmt, `go vet`,
golangci-lint and `go test -race`, all inside the same container stages the release image is built
from, so CI is not a second environment that can drift from what a developer sees. A second job
builds the production image, so a broken Dockerfile and a broken test are distinguishable at a
glance.

**`release.yml`** — on a `v*` tag. Runs the gates on the exact commit being published, builds each
architecture **on its own architecture**, and stitches them into one multi-arch tag. The version
baked into the binary comes from the tag, so `springback version` and `/api/health` report
something that corresponds to a commit.

`v0.11.0` publishes `:0.11.0`, `:0.11` and `:latest`, each carrying `linux/amd64` and
`linux/arm64`.

The three-job shape is not decoration. The first version built both platforms in one job with
QEMU, and this image compiles two things from source — ipatool needs `CGO_ENABLED=1`, so every one
of its dependencies goes through an emulated aarch64 toolchain. Measured on a real release:
**amd64 finished in minutes, arm64 was still compiling at 27**. Native runners make that a normal
build.

Each build job pushes an untagged image *by digest* and hands the digest to the merge job as an
artifact — not as a job output, because a matrix job has one set of outputs and each leg
overwrites the last, which would publish a "multi-arch" tag containing one architecture.

**The cost:** `ubuntu-24.04-arm` is free for public repositories and billed for private ones.
While this repo is private, either accept the charge, or go back to one runner with
`setup-qemu-action` and both platforms in a single build (slow but free), or drop `linux/arm64`,
which costs nothing but ARM users. netmuxd publishes an arm64 image, so the compose recipe in the
README works on ARM either way — checked against the registry.
