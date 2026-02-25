ALTER TABLE vm_templates ADD COLUMN vm_shortname TEXT NOT NULL DEFAULT '';

-- Seed the openclaw template.
INSERT INTO vm_templates (slug, title, short_description, category, prompt, icon_url, status, featured, vm_shortname)
VALUES (
    'openclaw',
    'Openclaw',
    'AI-powered remote development agent with web dashboard',
    'ai-ml',
    'ANTHROPIC_API_KEY=<fill-this-in>

Set up Openclaw (https://openclaw.ai/) on this VM. Openclaw used to be called Moltbot and before that Clawdbot, so be aware if the executable or other docs still refer to those names. Use the non-interactive and accept-risk flags for openclaw onboarding. Add the supplied auth or token as needed. Configure nginx to forward from the default port 18789 to the root location on the default enabled site config, making sure to enable Websocket support. Pairing is done by "openclaw devices list" and "openclaw device approve <request id>". Make sure the dashboard shows that Openclaw''s health is OK. exe.dev handles forwarding from port 8000 to port 80/443 and HTTPS for us, so the final "reachable" should be https://<vm-name>.exe.xyz without port specification.',
    'https://openclaw.ai/favicon.svg',
    'approved',
    1,
    'openclaw'
);
