"use client";

import { useEffect, useMemo, useState } from "react";
import { Alert, AlertDescription, AlertTitle } from "@shpyrd/ui/components/alert";
import { Button } from "@shpyrd/ui/components/button";
import { Card, CardContent } from "@shpyrd/ui/components/card";
import { Field } from "@shpyrd/ui/components/field";
import { Input } from "@shpyrd/ui/components/input";
import { PageHeading } from "@shpyrd/ui/components/page-heading";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@shpyrd/ui/components/select";
import { Stack } from "@shpyrd/ui/components/stack";

// The setup screen in front of Hermes: where the model comes from, the
// key, then the model chosen from what the provider lists for that key.
// hermes-setup (the Go program) answers under /setup/api.

type Provider = { id: string; name: string; keysAt: string; custom: boolean };

type State = {
  configured: boolean;
  provider?: string;
  model?: string;
  installKey: boolean;
  providers: Provider[];
  dashboardReady: boolean;
};

type Checked = { token: string; name: string; models: string[]; chosen?: string };

async function call<T>(path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, body === undefined
    ? { credentials: "same-origin" }
    : { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const data = (await res.json().catch(() => ({}))) as { error?: string } & T;
  if (!res.ok) throw new Error(data.error || `The setup answered ${res.status}.`);
  return data;
}

export default function App() {
  const [state, setState] = useState<State | null>(null);
  const [failed, setFailed] = useState("");
  const [editing, setEditing] = useState(false);
  // Opening Hermes as soon as it answers: after a Finish, or when the door
  // sent the page here because the dashboard was down (?starting). A visit
  // to /setup itself stays here, to change the provider or model.
  const [starting, setStarting] = useState(() => new URLSearchParams(window.location.search).has("starting"));

  useEffect(() => {
    call<State>("/setup/api/state").then(setState).catch((e: Error) => setFailed(e.message));
  }, []);

  if (failed) {
    return (
      <Page>
        <Alert variant="destructive">
          <AlertTitle>The setup could not be read</AlertTitle>
          <AlertDescription>{failed}</AlertDescription>
        </Alert>
      </Page>
    );
  }
  if (!state) return <Page><p className="text-muted-foreground">Loading…</p></Page>;
  if (state.configured && !editing) return <Starting state={state} starting={starting} onEdit={() => setEditing(true)} />;
  return <Setup state={state} onDone={(s) => { setState(s); setEditing(false); setStarting(true); }} />;
}

function Page({ children }: { children: React.ReactNode }) {
  return <main className="mx-auto w-full max-w-xl px-4 py-12">{children}</main>;
}

// Starting: Hermes is set up and its dashboard is being waited for. The
// state is read again every few seconds; when the dashboard answers, the
// address is Hermes.
function Starting({ state, starting, onEdit }: { state: State; starting: boolean; onEdit: () => void }) {
  const [ready, setReady] = useState(state.dashboardReady);
  useEffect(() => {
    if (ready) {
      if (starting) window.location.assign("/");
      return;
    }
    const t = window.setInterval(() => {
      call<State>("/setup/api/state").then((s) => { if (s.dashboardReady) setReady(true); }).catch(() => {});
    }, 3000);
    return () => window.clearInterval(t);
  }, [ready, starting]);
  const title = !ready ? "Hermes is starting" : starting ? "Opening Hermes" : "Hermes is set up";
  const description = !ready
    ? "The dashboard is not answering yet. This page tries again every few seconds."
    : starting
      ? "The dashboard answers; opening it."
      : "The dashboard answers at the project's address.";
  return (
    <Page>
      <Stack gap="spacious">
        <PageHeading title={title} description={description} />
        <Card>
          <CardContent>
            <Stack gap="cozy">
              <p>Hermes is set up with <strong>{state.provider}</strong> and the model <strong>{state.model}</strong>.</p>
              <Stack direction="horizontal" gap="cozy">
                {ready && !starting && <Button asChild><a href="/">Open Hermes</a></Button>}
                <Button variant="secondary" onClick={onEdit}>Change the provider or model</Button>
              </Stack>
            </Stack>
          </CardContent>
        </Card>
      </Stack>
    </Page>
  );
}

function Setup({ state, onDone }: { state: State; onDone: (s: State) => void }) {
  const providers = state.providers;
  const [provider, setProvider] = useState(state.provider && providers.some((p) => p.id === state.provider) ? state.provider : providers[0]?.id ?? "openrouter");
  const [base, setBase] = useState("");
  const [key, setKey] = useState("");
  const [checked, setChecked] = useState<Checked | null>(null);
  const [model, setModel] = useState("");
  const [filter, setFilter] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const chosen = providers.find((p) => p.id === provider);
  const custom = chosen?.custom ?? false;
  const installKey = state.installKey && provider === "openrouter";

  const check = async () => {
    setBusy(true);
    setError("");
    try {
      const c = await call<Checked>("/setup/api/check", { provider, key, base });
      setChecked(c);
      setModel(c.chosen && c.models.includes(c.chosen) ? c.chosen : c.models[0] ?? "");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const apply = async () => {
    if (!checked) return;
    setBusy(true);
    setError("");
    try {
      const s = await call<State>("/setup/api/apply", { token: checked.token, model });
      onDone(s);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const shown = useMemo(() => {
    if (!checked) return [];
    const f = filter.trim().toLowerCase();
    const list = f ? checked.models.filter((m) => m.toLowerCase().includes(f)) : checked.models;
    return list.includes(model) || !model ? list : [model, ...list];
  }, [checked, filter, model]);

  return (
    <Page>
      <Stack gap="spacious">
        <PageHeading
          title="Set up Hermes"
          description="Hermes needs a model to think with. Choose where it comes from and give the key; Telegram, Discord and the other channels are added later, from the dashboard or a shell."
        />
        {state.configured && (
          <Alert variant="info">
            <AlertDescription>Hermes is set up with {state.provider} and the model {state.model}. Choosing again replaces them.</AlertDescription>
          </Alert>
        )}
        {error && (
          <Alert variant="destructive" role="alert">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <Card>
          <CardContent>
            {checked ? (
              <Stack gap="normal">
                <p>The key works. {checked.name} lists {checked.models.length} models.</p>
                {checked.models.length > 12 && (
                  <Field label="Find a model" labelHidden>
                    <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Filter the models" />
                  </Field>
                )}
                <Field label="Model" hint="The model Hermes answers with. It can be changed later, here or in the dashboard. Hermes wants a context of 64,000 tokens or more." required>
                  <Select value={model} onValueChange={setModel}>
                    <SelectTrigger aria-label="Model" className="w-full"><SelectValue placeholder="Choose a model" /></SelectTrigger>
                    <SelectContent>
                      {shown.slice(0, 400).map((m) => <SelectItem key={m} value={m}>{m}</SelectItem>)}
                    </SelectContent>
                  </Select>
                </Field>
                <Stack direction="horizontal" gap="cozy">
                  <Button onClick={apply} disabled={busy || !model}>{busy ? "Finishing…" : "Finish"}</Button>
                  <Button variant="ghost" onClick={() => { setChecked(null); setFilter(""); }} disabled={busy}>Use another provider or key</Button>
                </Stack>
              </Stack>
            ) : (
              <Stack gap="normal">
                <Field label="Provider">
                  <Select value={provider} onValueChange={(v) => { setProvider(v); setError(""); }}>
                    <SelectTrigger aria-label="Provider" className="w-full"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      {providers.map((p) => <SelectItem key={p.id} value={p.id}>{p.name}</SelectItem>)}
                    </SelectContent>
                  </Select>
                </Field>
                {custom && (
                  <Field label="Server address" hint="An OpenAI-compatible server: Ollama, vLLM, LM Studio, or a gateway. The address of its /v1." required>
                    <Input type="url" value={base} onChange={(e) => setBase(e.target.value)} placeholder="https://llm.example.com/v1" />
                  </Field>
                )}
                <Field
                  label="API key"
                  hint={custom
                    ? "Leave empty when the server asks for none."
                    : installKey
                      ? "An OpenRouter key was given when the project was installed. Leave this empty to use it."
                      : `From ${chosen?.keysAt ?? "the provider"}. Kept in Hermes' own files, never shown again.`}
                  required={!custom && !installKey}
                >
                  <Input type="password" autoComplete="off" spellCheck={false} value={key} onChange={(e) => setKey(e.target.value)} />
                </Field>
                <div>
                  <Button onClick={check} disabled={busy || (!custom && !installKey && !key) || (custom && !base)}>{busy ? "Checking…" : "Check the key"}</Button>
                </div>
              </Stack>
            )}
          </CardContent>
        </Card>
      </Stack>
    </Page>
  );
}
