# Gerege Client's own image.
#
# Two binaries: this product's server — the platform plus its modules, of which
# there are none yet — and the platform's migration runner, built from the exact
# core version go.mod pins. The migrations and the catalogue come from that same
# version, so the schema an image applies and the apps it offers are the ones
# the code in it was written against.

FROM golang:1.26-alpine AS build

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Dependencies first, so a change to our own code does not re-download the core.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# What this build tells an app store it is.
#
# Derived from the core version go.mod pins rather than passed in, because it is
# not a choice: PlatformVersion is what every manifest's `"platform": ">=1.0.0"`
# is checked against, and the honest answer is the version of the platform
# actually compiled into this image. Left to a build argument it defaults to
# 0.0.0, and the instance then refuses every manifest it ships with — it cannot
# read its own catalogue, so it will not start.
RUN core=$(go list -m -f '{{.Version}}' github.com/gerege-systems/open-gerege-nexus/backend) \
    && version=${core#v} \
    && echo "building as platform ${version}" \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-w -s -X github.com/gerege-systems/open-gerege-nexus/backend/internal/platform.PlatformVersion=${version}" \
    -o /bin/clientnexus .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-w -s" \
    -o /bin/migrate github.com/gerege-systems/open-gerege-nexus/backend/cmd/migrate

# The platform's migration files, which are data in the module rather than code.
# The path carries the version, so this reads it back rather than guessing.
RUN core=$(go list -m -f '{{.Dir}}' github.com/gerege-systems/open-gerege-nexus/backend) \
    && mkdir -p /out/db && cp -r "$core/db/migrations" /out/db/migrations \
    && chmod -R u+w /out/db

# The catalogue, fetched at the same pinned version rather than copied into this
# repository.
#
# It cannot come out of the module cache like the migrations do: the Go module
# is rooted at the core's `backend/` and `catalog/` sits beside it, outside the
# module. So it comes from the tag `go list` just reported — the alternative is
# nine JSON files committed here that go stale the day the core is bumped, which
# is exactly the copying §5 rule 2 forbids.
#
# Every entry in it is a platform app. The day this repository compiles a module
# of its own, that app is added to a catalogue of ours and this step becomes a
# merge instead of a copy.
RUN core=$(go list -m -f '{{.Version}}' github.com/gerege-systems/open-gerege-nexus/backend) \
    && wget -qO- "https://github.com/gerege-systems/open-gerege-nexus/archive/refs/tags/backend/${core}.tar.gz" \
    | tar -xz -C /tmp \
    && cp -r /tmp/open-gerege-nexus-*/catalog /out/catalog \
    && test -f /out/catalog/apps.json

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata wget \
    && adduser -D -u 10001 clientnexus

WORKDIR /app

COPY --from=build /bin/clientnexus /app/clientnexus
COPY --from=build /bin/migrate  /app/migrate
COPY --from=build /out/db       /app/db
COPY --from=build /out/catalog  /app/catalog

USER clientnexus
EXPOSE 8080

CMD ["/app/clientnexus"]
