# Gerege Client's own image.
#
# Two binaries: this product's server — the platform plus the modules this
# repository carries — and the platform's migration runner, built from the exact
# core version go.mod pins. The platform's migrations and half of the catalogue
# come from that same version, so the schema an image applies and the apps it
# offers are the ones the code in it was written against. A module's own
# migrations do not: they travel inside the module, through nexus.Migrations.

FROM golang:1.26-alpine AS build

RUN apk add --no-cache git ca-certificates jq

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

# The catalogue: the core's, merged with this repository's own.
#
# The core's half cannot come out of the module cache like the migrations do:
# the Go module is rooted at the core's `backend/` and `catalog/` sits beside
# it, outside the module. So it comes from the tag `go list` just reported —
# the alternative is committing the platform's entries here, where they go
# stale the day the core is bumped, which is the copying §5 rule 2 forbids.
#
# The merge is the day this file predicted: this repository compiles modules of
# its own now, and their entries live in `catalog/`. A copy would drop them,
# and an instance whose binary carries an app its catalogue does not name
# refuses to start — `Options.DefaultApps` is checked against the catalogue
# while the server is built.
#
# A module with no manifest or no chronicle of its own is allowed — the
# directories are copied only when they exist, and a copy that then fails
# still fails the build rather than shipping a catalogue missing an entry.
#
# Ours first, so a duplicate id resolves to this repository's entry: jq's sort
# is stable and `unique_by` keeps the first of each group. That matters while a
# module is mid-move — the app is in both catalogues for exactly as long as it
# takes the core to drop it, and during that window the entry that describes
# the code in this image is ours.
RUN core=$(go list -m -f '{{.Version}}' github.com/gerege-systems/open-gerege-nexus/backend) \
    && wget -qO- "https://github.com/gerege-systems/open-gerege-nexus/archive/refs/tags/backend/${core}.tar.gz" \
    | tar -xz -C /tmp \
    && cp -r /tmp/open-gerege-nexus-*/catalog /out/catalog \
    && if [ -d catalog/manifests ]; then mkdir -p /out/catalog/manifests \
       && cp -f catalog/manifests/*.json /out/catalog/manifests/; fi \
    && if [ -d catalog/chronicle ]; then mkdir -p /out/catalog/chronicle \
       && cp -f catalog/chronicle/*.json /out/catalog/chronicle/; fi \
    && jq -s 'add | unique_by(.id)' catalog/apps.json /tmp/open-gerege-nexus-*/catalog/apps.json \
       > /out/catalog/apps.json \
    && test -f /out/catalog/apps.json \
    && for id in $(jq -r '.[].id' catalog/apps.json); do \
         jq -e --arg id "$id" 'map(select(.id == $id)) | length == 1' /out/catalog/apps.json \
         > /dev/null || { echo "catalogue merge lost $id"; exit 1; }; \
       done

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
