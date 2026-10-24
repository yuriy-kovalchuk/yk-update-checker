# Fonts

JetBrains Mono Nerd Font 3.5.1 (JetBrains Mono 2.304), patched by Nerd Fonts
(https://www.nerdfonts.com/, `ryanoasis/nerd-fonts`), licensed under the SIL Open
Font License 1.1 — see `LICENSE-JetBrainsMono.txt`.

The `.woff2` files are subsets, not the upstream binaries:

| codepoints | why |
|---|---|
`U+0020-007E`, `U+00A0-00FF`, punctuation/dashes/ellipsis | UI text |
`U+2192` | arrow |
`U+2500-257F`, `U+2580-259F` | box drawing and block elements |
`U+E0A0-E0DF` | Powerline separators |
`U+F000-F2FF` | Nerd Font (FontAwesome 4/5) icons used by the dashboard |

Layout features kept: `kern`, `calt`, `tnum`, `zero`. Hinting, `DSIG` and the
vertical-metric tables were dropped — `post`, `cmap`, `head`, `hhea`, `hmtx`,
`maxp`, `name` and `OS/2` must stay, otherwise Chrome's OpenType sanitizer
rejects the file and the UI silently falls back to a system mono. Two weights
only: Regular (400) and SemiBold (600), matching the `font-weight` values in
`app.css`.

Regenerate (≈215 KB total):

```bash
FC="U+0020-007E,U+00A0-00FF,U+2013-2014,U+2018-2019,U+201C-201D,U+2026,U+2192,\
U+2500-257F,U+2580-259F,U+E0A0-E0DF,U+F000-F2FF"
for w in Regular SemiBold; do
  pyftsubset JetBrainsMonoNerdFont-$w.ttf \
    --output-file=JetBrainsMonoNF-$w.woff2 --flavor=woff2 \
    --unicodes="$FC" --layout-features='kern,calt,tnum,zero' \
    --no-hinting --desubroutinize --drop-tables+=DSIG,LTSH,VDMX,hdmx
done
```

Served by `internal/dashboard/handler.go` under `/fonts/`. The whole `U+F000-F2FF`
icon block is kept rather than only the 24 glyphs the UI uses today, so adding an
icon later cannot produce a tofu box.
