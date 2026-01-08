// This script manages Grafana notification templates and contact point configurations.
//
// To get a GRAFANA_BEARER_TOKEN, visit
// https://grafana.crocodile-vector.ts.net/org/serviceaccounts
//
// Run with:
//   GRAFANA_URL=https://grafana.crocodile-vector.ts.net/ ./node_modules/.bin/npx tsx notifications.mts
//
// Or via Makefile:
//   make deploy-notifications

import { fileURLToPath } from "node:url";

const TOKEN = process.env.GRAFANA_BEARER_TOKEN;
const GRAFANA_URL = process.env.GRAFANA_URL || "https://grafana.crocodile-vector.ts.net/";

// Ensure GRAFANA_URL ends with a slash
const baseUrl = GRAFANA_URL.endsWith("/") ? GRAFANA_URL : GRAFANA_URL + "/";

// Notification templates define reusable message formats.
// These use Go templating syntax.
// See: https://grafana.com/docs/grafana/latest/alerting/configure-notifications/template-notifications/
const notificationTemplates: Record<string, string> = {
  // Clean Slack message template
  // Produces: "[FIRING] exe-ctr-11: Swap usage is high"
  // With description, labels, and link
  "exe-slack": `{{ define "exe-slack.title" -}}
[{{ .Status | toUpper }}] {{ with (index .Alerts 0) -}}
{{ .Labels.instance | reReplaceAll ":.*" "" }}: {{ .Annotations.summary }}
{{- end }}
{{- end }}

{{ define "exe-slack.text" -}}
{{ range .Alerts -}}
{{ .Annotations.description }}
{{ range .Labels.SortedPairs -}}{{ .Name }}={{ .Value }} {{ end }}
{{ if .GeneratorURL }}<{{ .GeneratorURL }}&tab=query|View Alert>{{ end }}
{{- end }}
{{- end }}`,
};

// Contact point message template assignments.
// Maps contact point name to template settings.
// Note: We don't store webhook URLs here - those are configured in Grafana UI.
const contactPointTemplates: Record<string, { title: string; text: string }> = {
  "exe.dev slack #page": {
    title: `{{ template "exe-slack.title" . }}`,
    text: `{{ template "exe-slack.text" . }}`,
  },
  "exe.dev slack #page-dev": {
    title: `{{ template "exe-slack.title" . }}`,
    text: `{{ template "exe-slack.text" . }}`,
  },
  "exe.dev slack #poke": {
    title: `{{ template "exe-slack.title" . }}`,
    text: `{{ template "exe-slack.text" . }}`,
  },
};

async function apiFetch(
  path: string,
  options: RequestInit = {}
): Promise<Response> {
  const url = `${baseUrl}${path}`;
  const response = await fetch(url, {
    ...options,
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${TOKEN}`,
      ...options.headers,
    },
  });
  return response;
}

async function createOrUpdateTemplate(name: string, template: string) {
  console.log(`Updating template: ${name}`);

  const response = await apiFetch(`api/v1/provisioning/templates/${name}`, {
    method: "PUT",
    body: JSON.stringify({ template }),
  });

  if (!response.ok) {
    const text = await response.text();
    throw new Error(`Failed to update template ${name}: ${response.status} ${text}`);
  }

  console.log(`  ✓ Template ${name} updated`);
}

async function getContactPoints(): Promise<
  Array<{
    uid: string;
    name: string;
    type: string;
    settings: Record<string, unknown>;
    disableResolveMessage: boolean;
  }>
> {
  const response = await apiFetch("api/v1/provisioning/contact-points");
  if (!response.ok) {
    throw new Error(`Failed to get contact points: ${response.status}`);
  }
  return response.json();
}

async function updateContactPoint(
  uid: string,
  name: string,
  type: string,
  settings: Record<string, unknown>,
  disableResolveMessage: boolean
) {
  console.log(`Updating contact point: ${name}`);

  const response = await apiFetch(`api/v1/provisioning/contact-points/${uid}`, {
    method: "PUT",
    body: JSON.stringify({
      uid,
      name,
      type,
      settings,
      disableResolveMessage,
    }),
  });

  if (!response.ok) {
    const text = await response.text();
    throw new Error(`Failed to update contact point ${name}: ${response.status} ${text}`);
  }

  console.log(`  ✓ Contact point ${name} updated`);
}

async function main() {
  if (!TOKEN) {
    console.error(
      "Please provide a Grafana bearer token in the GRAFANA_BEARER_TOKEN environment variable."
    );
    process.exit(1);
  }

  console.log("Deploying notification templates to", baseUrl);
  console.log();

  // 1. Create/update notification templates
  console.log("=== Notification Templates ===");
  for (const [name, template] of Object.entries(notificationTemplates)) {
    await createOrUpdateTemplate(name, template);
  }
  console.log();

  // 2. Update contact points to use templates
  console.log("=== Contact Points ===");
  const contactPoints = await getContactPoints();

  for (const cp of contactPoints) {
    const templateConfig = contactPointTemplates[cp.name];
    if (!templateConfig) {
      continue; // Skip contact points we don't manage
    }

    // Merge template settings with existing settings (preserves webhook URL)
    const updatedSettings = {
      ...cp.settings,
      title: templateConfig.title,
      text: templateConfig.text,
    };

    await updateContactPoint(
      cp.uid,
      cp.name,
      cp.type,
      updatedSettings,
      cp.disableResolveMessage
    );
  }

  console.log();
  console.log("Done!");
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  await main();
}
