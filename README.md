# Hermes Agent on shpyrd

[Hermes Agent](https://hermes-agent.nousresearch.com) by Nous Research,
packaged as a project for [shpyrd](https://github.com/shpyrd-io/shpyrd),
installed from the catalog's templates. The official image, plus one
program in front of it: a setup screen on first visit, then Hermes' own
dashboard.

## What the person sees

1. The project is installed from the templates. Nothing is asked but a
   name.
2. The first visit shows **Set up Hermes**: a provider (OpenRouter,
   Anthropic, or any OpenAI-compatible server), the key, then the model,
   chosen from the list the provider answers with for that key.
3. From then on the address is Hermes' dashboard: chat, sessions, config,
   skills, channels, logs. `/setup` reopens the setup to change provider,
   key or model.
4. Messaging comes later. In the dashboard's **Channels** page, or from a
   shell on the project (`shpyrd shell <project>`) with `hermes gateway
   setup`, the way for WhatsApp's QR code.

The platform's sign-in is the only login: the project's access is
"authenticated", people with a role on the project, and the dashboard
listens on loopback inside the container, where it asks for none of its
own. Do not set the project's access to public.

## How it runs

`hermes-setup` (this repository, Go, no dependencies) is the container's
process. The setup page is `web/`, a Next application on the shpyrd
design system (`@shpyrd/ui`), compiled to static files under `/setup`
and embedded in the program; the program answers its API under
`/setup/api`. Hermes' home is `/opt/data/hermes`, a directory the program makes
inside the project's disk: the disk's root belongs to root, and Hermes
refuses a home it does not own. The program listens on `$PORT` and:

- until `/opt/data/hermes/.shpyrd/setup.json` exists, shows the setup and
  redirects everything else to it;
- writes the choice through `hermes config set` (provider and model to
  `config.yaml`, the key to `.env`, as Hermes itself does);
- then runs `hermes dashboard` on 127.0.0.1:9119 and `hermes gateway run`
  as children, starting them again when they exit, and proxies to the
  dashboard. A setup run again restarts the gateway, which reads the
  model at start; channels added later are picked up by the dashboard's
  own **Restart gateway**, as in any Hermes.

As PID 1 the program first runs itself again as a child and stays as its
init: it waits for the processes Hermes leaves behind, while the program
waits for its own children only.

Hermes' own supervisor, s6, is not used: the platform runs the image as
the hermes user with no way to root, and s6's bootstrap wants root.

## Updating Hermes

The `FROM nousresearch/hermes-agent:<tag>` line in the Dockerfile. Every
push to main is built and released on the projects that follow it.

## Developing

    (cd web && npm ci && npm run build)   # the page, embedded by the program
    go test ./...
    docker build -t hermes-shpyrd .
    docker run --rm -p 8080:8080 -v hermes-data:/opt/data hermes-shpyrd
