# Fonts

Roboto and Roboto Mono, variable (latin range), as served by Google Fonts. Roboto
is the closest freely licensed match to the Google Cloud console typography the
dashboard's style follows, and Roboto Mono carries the technical values — versions,
digests, timestamps — that the console sets in a mono face.

| File | Family | Weight range | Size |
|---|---|---|---|
| `roboto-latin-var.woff2` | Roboto | 100–900 | 40 KB |
| `roboto-mono-latin-var.woff2` | Roboto Mono | 100–700 | 33 KB |

Both are variable, so one file per family covers every weight `app.css` asks for.
The latin `unicode-range` in `app.css` keeps the fallback stack for other scripts.

Licence: SIL Open Font License 1.1, with "Roboto" and "Roboto Mono" as Reserved Font
Names — the licence text is `LICENSE-Roboto.txt` and `LICENSE-RobotoMono.txt`, taken
from the upstream catalog (`googlefonts/roboto-classic`, `googlefonts/robotomono`).
Each `.woff2` says the same in its own `name` table (name ID 14 → openfontlicense.org).

Served by `internal/dashboard/handler.go` under `/fonts/`, cached for a week
(`immutable`), and the CSP is `font-src 'self'`, so nothing here may load from a CDN —
that is also why the files are vendored: the dashboard renders identically on an
air-gapped cluster.

Icons are **not** fonts: every glyph in the UI is an inline SVG Material icon
(`ICONS` in `app.js`, Apache 2.0). The dashboard used a JetBrains Mono Nerd Font
subset before, which forced a monospace UI and could render a tofu box for a glyph
outside the subset.
