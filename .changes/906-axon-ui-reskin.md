### feat(ui): reskin the dashboard to the AxonRouter design system

The dashboard still wore the upstream 9router skin: terracotta brand
(`#e56a4a`), warm paper light surfaces, frosted translucent sidebar,
10/14px brand radii and soft warm shadows. It now carries the AxonRouter
design system end to end, inside the existing Svelte 5 + Vite + Tailwind
v4 framework. No component logic, state, routing or API code changed;
the reskin is tokens, global styles and class attributes only.

**Tokens (`web/src/index.css`).** The brand scale is Axon's cyan scale
(500 `#06B6D4`). The dark theme is Axon's palette verbatim: bg `#000000`,
bg-alt `#0A0A0A`, surfaces `#121212`/`#1A1A1A`/`#242424`, solid sidebar
`#0A0A0A`, borders `#2A2A2A`/`#1F1F1F`, text `#FFFFFF`/`#A0A0A0`/`#7A7A7A`,
danger `#FF5555`, success `#22C55E`, warning `#FFB86C`. The light theme
uses Axon's own light mapping (its `colors.js`): cool slate surfaces
(`#F4F7F8`/`#E7EEEF`/`#FFFFFF`), primary `#0891B2`. Per-theme values moved
behind `--app-*` variables (primary, status, shadows, table colours) so
both themes stay exact. Radii drop to Axon's: `--radius-brand` 4px,
`--radius-brand-lg` 6px, and the Tailwind radius scale is overridden to
4/6/8/10/12px. Ported Axon global styles: thin global scrollbars, cyan
selection and focus ring, `.data-table` family (grid lines, zebra rows
from the new `--color-table-*` tokens), `.data-cards`, `.card-soft`,
`.card-elev`, `.dot-grid-bg`, `.btn-cta`, fade/slide utilities and the
reduced-motion guard.

**Shell and primitives.** Sidebar is the Axon shell: solid `bg-sidebar`,
`w-64`, `h-16` header row, nav items `h-8` at 13px with
`bg-primary/10 text-primary` active state, 9px uppercase section labels;
the macOS traffic-light dots are gone from the sidebar and from every
modal header (the shared Modal, ModelPickerModal and AddConnectionModal
now close through one ghost icon button that works at every width).
TopBar is a solid `h-16 bg-bg` bar with Axon's search field metrics.
Buttons, cards, inputs, badges, menus, toggles and toasts in `lib/ui`
follow the same metrics (primary cyan, `rounded-sm`, focus
`ring-primary/15` + `border-primary/60`). All `backdrop-blur` is removed
(26 occurrences): backdrops are solid `bg-black/60`, surfaces are opaque.
Views were swept: 82 `rounded-[10px]` became `rounded-brand` and 45
`rounded-[14px]` became `rounded-brand-lg`; `web/index.html` boots on
black with a cyan selection tint.

**Bundle (§6.F numbers, same tree before/after).** dist total 4.4 MB to
4.5 MB. CSS 124,917 to 133,146 bytes raw (+6.6%; brotli 15,985 to
16,960): the growth is the ported Axon global stylesheet above. JS
1,518,349 to 1,515,336 bytes (smaller: the traffic-light markup is gone).
No new dependencies.

**Verification.** `bun run build` passes. svelte-check reports the
identical 78 errors before and after this change (zero new, zero in the
fatal unresolved-identifier class). Note the pinned baseline in
`web/scripts/svelte-check-baseline.json` says 75, but a pristine check of
this fork's HEAD also reports 78: the baseline went stale when the fork
merged upstream, before this change. It is deliberately not widened
here; updating it belongs with fixing those three pre-existing errors.
