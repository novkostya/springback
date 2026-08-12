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

## The action versions are checked, not remembered

Both files pin every action to a major, and those pins were wrong on the first attempt: written
from memory they came out as `actions/checkout@v4` and each `docker/*` action one major behind,
all targeting a Node 20 runtime GitHub has since deprecated. There was no reason for it beyond v4
being what most workflow YAML in the world still says.

[`.github/dependabot.yml`](../../.github/dependabot.yml) now watches them weekly and groups the
updates into one pull request. To check by hand:

```sh
for r in actions/checkout docker/setup-qemu-action docker/setup-buildx-action \
         docker/login-action docker/metadata-action docker/build-push-action; do
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

**`release.yml`** — on a `v*` tag. Runs the gates again on the exact commit being published, then
builds and pushes to `ghcr.io/<owner>/springback` for `linux/amd64` and `linux/arm64`. The version
baked into the binary comes from the tag, so `springback version` and `/api/health` report
something that corresponds to a commit.

`v0.11.0` publishes `:0.11.0`, `:0.11` and `:latest`.

**One thing to watch on the first release:** the arm64 image is built under QEMU emulation, and
that stage compiles ipatool and ideviceinstaller from source. Expect it to be slow — tens of
minutes — and if it turns out to be unreliable, dropping `linux/arm64` from `platforms` costs
nothing but ARM users.
