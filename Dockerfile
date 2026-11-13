# Hermes Agent for shpyrd: the official image, with the setup screen in
# front of the dashboard. The platform injects PORT and mounts the data
# volume at /opt/data, Hermes' home.

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/hermes-setup .

# The Hermes release. Bumping this tag is how Hermes is updated.
FROM nousresearch/hermes-agent:v0.23.1
COPY --from=build /out/hermes-setup /usr/local/bin/hermes-setup
# The platform runs images as a user, never root; hermes is uid 10000 in
# the image. The data volume is handed to the process's group by the
# platform, so the image's own chown at boot is not needed.
USER 10000:10000
# The volume is mounted at /opt/data, whose root belongs to root; Hermes'
# home is a directory of its own inside it, made by hermes-setup (Hermes
# refuses a home it does not own).
ENV HERMES_HOME=/opt/data/hermes HOME=/opt/data/hermes PORT=8080
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/hermes-setup"]
