# Project logos

Icons that label which kind of manifest each dashboard section covers. Three come
unmodified from the CNCF artwork repository (https://github.com/cncf/artwork):

| File | Source |
|---|---|
| `kubernetes.svg` | `projects/kubernetes/icon/color/kubernetes-icon-color.svg` |
| `flux.svg` | `projects/flux/icon/color/flux-icon-color.svg` |
| `helm.svg` | `projects/helm/icon/white/helm-icon-white.svg` (the white variant; the marks are drawn in one colour — see below) |
| `docker.svg` | `simple-icons` (https://github.com/simple-icons/simple-icons), `icons/docker.svg` — path data under CC0-4.0 |

`docker.svg` is the one logo that is not CNCF artwork: Docker is a trademark of
Docker Inc., and the mark is used here only to identify the kind of file being
scanned. Its single path carries `fill="#2496ED"` (Docker blue): an SVG loaded
through `<img>` falls back to black when the file names no colour.

`app.css` draws all four marks in one colour (a `brightness(0)` filter plus a per-theme
`invert`), because the console's own icons are monochrome and a navy or a white mark would
be invisible in one of the two palettes. The SVG files themselves are never edited.

The CNCF artwork is made available under the Linux Foundation trademark usage
guidelines (https://www.linuxfoundation.org/trademark-usage/). It is used here
only to identify the projects whose files are scanned, never as this
project's own logo, and it must not suggest endorsement by the Linux
Foundation or its projects.
