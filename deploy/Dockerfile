# springback — single multi-stage image. Also the source of the dev toolchain image: `make`
# builds the `toolchain-go` stage and runs every gate inside it, so dev and this release image
# compile with byte-identical toolchains. All pins come from versions.env via --build-arg
# (defaults below mirror it).
#
# The image carries Apple's userland (libimobiledevice + ideviceinstaller) and ipatool, because
# springback shells out to all of them. It deliberately does NOT carry usbmuxd — see the runtime
# stage for why.

# ---- pinned base images (mirror versions.env) — ONE Alpine line (3.24) everywhere ----
ARG GO_IMAGE=golang:1.26.5-alpine3.24
ARG ALPINE_IMAGE=alpine:3.24

# THE VERSION PINS HAVE NO DEFAULTS, DELIBERATELY, AND THAT NEEDS A GUARD.
#
# Two base images carry defaults above because a FROM cannot be checked at run time; everything
# else is passed from versions.env, which stays the single source of truth. The cost is that a
# caller who forgets one gets an EMPTY string rather than an error — and an empty pin does not
# fail where it was omitted, it fails several lines later inside somebody else's tool.
#
# Measured, from a real release run: a build-push-action that passed only VERSION produced
#
#     git clone --depth 1 --branch  https://github.com/libimobiledevice/ideviceinstaller .
#     ERROR: process "/bin/sh -c git clone ..." did not complete successfully: exit code: 128
#
# — git reading the URL as the branch name. Nothing in that says "you forgot a build argument",
# and it cost an afternoon. Each stage below now checks its own pin first and says which one.
ARG PIN_HINT="pass it from versions.env, e.g. --build-arg NAME=value (the Makefile does this for you)"

# =============================================================================
# Toolchain stage — reused verbatim by the Makefile gate targets.
# =============================================================================
FROM ${GO_IMAGE} AS toolchain-go
ARG GOLANGCI_LINT_VERSION
ARG PIN_HINT
RUN test -n "${GOLANGCI_LINT_VERSION}" || { echo "GOLANGCI_LINT_VERSION is empty — ${PIN_HINT}"; exit 1; }
RUN apk add --no-cache git make gcc musl-dev
# golangci-lint built WITH this exact Go, so no analyzer/toolchain skew. v2 module path carries
# the /v2 suffix.
RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v${GOLANGCI_LINT_VERSION}
WORKDIR /src

# =============================================================================
# Build stages.
# =============================================================================
# --- ipatool: the Apple Store client, pinned, built against musl ---
#
# CGO_ENABLED=1, WHICH IS NOT THE OBVIOUS CHOICE and is forced rather than preferred. ipatool
# 2.3.2 depends on the 1Password SDK for its keyring backends, and that module fails to compile
# with cgo off — it resolves to a stub whose only symbol is the compile error
# `ERROR_WithDesktopAppIntegration_requires_CGO_To_Cross_Compile_See_README_CGO_Section`.
# Measured at pin time; there is no build tag that avoids it.
#
# Safe here because this is a SAME-PLATFORM build, not a cross-compile: this stage and the
# runtime stage are both Alpine 3.24/musl (versions.env keeps one Alpine line across the whole
# image for exactly this class of reason), so the resulting dynamic link is against the musl and
# libgcc that the runtime already has. `springback` itself stays CGO_ENABLED=0.
FROM toolchain-go AS ipatool-build
ARG IPATOOL_REF
ARG PIN_HINT
RUN test -n "${IPATOOL_REF}" || { echo "IPATOOL_REF is empty — ${PIN_HINT}"; exit 1; }
RUN CGO_ENABLED=1 go install -trimpath -ldflags "-s -w" \
      github.com/majd/ipatool/v2@${IPATOOL_REF} \
    && install -m0755 /go/bin/ipatool /ipatool

# --- ideviceinstaller: pinned tag, built from source (Alpine 3.24 does not package it) ---
#
# Everything it links against IS packaged at 3.24 — libimobiledevice 1.4.0, libplist 2.6,
# libimobiledevice-glue, libzip — so only this one repo is built. See versions.env for the
# APKINDEX check behind that claim.
FROM ${ALPINE_IMAGE} AS ideviceinstaller-build
ARG IDEVICEINSTALLER_REF
ARG PIN_HINT
RUN test -n "${IDEVICEINSTALLER_REF}" || { echo "IDEVICEINSTALLER_REF is empty — ${PIN_HINT}"; exit 1; }
RUN apk add --no-cache git build-base autoconf automake libtool pkgconf \
      libimobiledevice-dev libplist-dev libimobiledevice-glue-dev libzip-dev
WORKDIR /src/ideviceinstaller
RUN git clone --depth 1 --branch ${IDEVICEINSTALLER_REF} \
      https://github.com/libimobiledevice/ideviceinstaller .
# Install to a DESTDIR the runtime stage copies from. autogen.sh runs ./configure "$@" only when
# NOCONFIGURE is unset — do not set it.
RUN ./autogen.sh --prefix=/usr \
    && make -j"$(nproc)" \
    && make DESTDIR=/stage install

# --- sbicon: the one device call libimobiledevice ships no tool for ---
#
# SpringBoard services can hand back the icon a device draws for an installed app, which is the
# only icon source that also covers DELISTED apps — the store has no artwork for them and, for
# anything not archived, there is no .ipa to look inside either. No shipped CLI exposes it, so
# springback builds its own. Same libraries and same Alpine as ideviceinstaller above, so this
# stage adds a compile and no new packages to the runtime.
FROM ${ALPINE_IMAGE} AS sbicon-build
RUN apk add --no-cache build-base pkgconf libimobiledevice-dev libplist-dev libimobiledevice-glue-dev
COPY deploy/sbicon.c deploy/sbwifi.c /src/
RUN cc -O2 -Wall -Wextra -Werror -o /sbicon /src/sbicon.c \
      $(pkg-config --cflags --libs libimobiledevice-1.0) \
 && cc -O2 -Wall -Wextra -Werror -o /sbwifi /src/sbwifi.c \
      $(pkg-config --cflags --libs libimobiledevice-1.0)

# --- core: embed the static UI, cgo-free static binary ---
FROM toolchain-go AS core-build
ARG VERSION=0.0.0-dev
WORKDIR /src/core
COPY core/go.mod ./
COPY core/go.sum* ./
RUN go mod download
COPY core/ ./
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/novkostya/springback/core/internal/version.Version=${VERSION}" \
      -o /springback ./cmd/springback

# =============================================================================
# Runtime — Alpine + the Apple userland springback shells out to.
# =============================================================================
FROM ${ALPINE_IMAGE} AS runtime
# THERE IS NO usbmuxd HERE, AND ITS ABSENCE IS THE POINT (SPEC §2). The HOST runs one and owns
# the USB bus; a second daemon inside a container fights it for the same devices. springback
# reaches devices through the host's muxer socket, or over netmuxd via USBMUXD_SOCKET_ADDRESS.
# Installing the package "just in case" would put a startable competitor inside the image, and
# the failure it causes lands on whatever else is managing the devices.
#
#   libimobiledevice-progs = idevice_id + ideviceinfo (Alpine 3.24 ships 1.4.0). springback needs
#                            no patched build: it reads device name/region and lists udids.
#
#   libzip                 = runtime link dep of the ideviceinstaller built above.
#   ca-certificates        = TLS for the iTunes lookup API and ipatool's Store calls.
RUN apk add --no-cache \
      ca-certificates tzdata \
      libimobiledevice-progs \
      libimobiledevice-glue \
      libusbmuxd \
      libplist \
      libtatsu \
      libzip

COPY --from=ipatool-build /ipatool /usr/local/bin/ipatool
COPY --from=ideviceinstaller-build /stage/usr/bin/ideviceinstaller /usr/local/bin/ideviceinstaller
COPY --from=sbicon-build /sbicon /usr/local/bin/sbicon
COPY --from=sbicon-build /sbwifi /usr/local/bin/sbwifi
COPY --from=core-build /springback /usr/local/bin/springback

ENV SPRINGBACK_LIBRARY=/library \
    SPRINGBACK_ACCOUNTS=/accounts \
    SPRINGBACK_LISTEN=:8971

# THE IMAGE MUST CONTAIN THE DIRECTORIES IT DECLARES — learned on a deployment with
# no mounts, where the binary died on an errno naming neither the missing directory nor the
# variable that pointed at it. Both paths are bind-mounted in every real deployment, which is
# exactly why the defaults would otherwise be dead config that reads as live.
RUN mkdir -p /library /accounts

EXPOSE 8971
ENTRYPOINT ["springback"]
CMD ["serve"]
