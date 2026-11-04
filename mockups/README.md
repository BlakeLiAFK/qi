# qi UI mockup

Open `index.html` directly in a browser. It is a self-contained, offline Chinese-language design prototype, not the production Web UI.

## Included

- Desktop three-column workspace and responsive mobile layout.
- Three independently switchable task examples, with separate drafts and local chat messages.
- Feishu-inspired deliverable, explicit approval, and question cards.
- Plugin center and configuration views.
- Visible mock-data labels. No model calls, remote connections, credentials, real accounts, uploads, sharing, bookings, or permissions changes.

Demo messages stay only in page memory and reset on reload. All external-action buttons display a simulation notice.

## Screenshots and interaction checks

`capture.cjs` captures desktop, mobile, plugin, configuration and question-card screenshots using Playwright. It also checks task draft/message isolation, JavaScript errors, and horizontal overflow on mobile.

Run in a Chromium-capable environment with Playwright installed:

```sh
node mockups/capture.cjs
# To use a system Chromium installation:
CHROMIUM_PATH=/usr/bin/chromium node mockups/capture.cjs
```

At authoring time, inline JavaScript syntax was validated, but browser launch in the cloud execution environment failed before navigation with `socket() failed: Operation not permitted`. No screenshots have been fabricated. Screenshots and browser interaction checks remain pending until run in a permitted browser environment.
