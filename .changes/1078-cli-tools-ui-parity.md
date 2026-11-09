### Added
- CLI Tools page gains a stat row (Visible Tools / Installed / Connected) mirroring the JS dashboard's summary cards, with counts derived from the live tool-status scan instead of static numbers.
- Category tabs now carry per-category counts, and a legend under the filters explains the status badges (Connected / Not configured / Not installed / Guide) and the method labels (Environment / Settings UI / Transparent).

### Fixed
- Tool detail modal could trap the user: the panel had no height bound or scroll area, so on shorter viewports its header, close button and footer rendered off-screen, and neither Escape nor a backdrop click dismissed it. The panel is now a column layout capped at 90dvh with pinned header and footer and a scrolling body, Escape closes it, and clicking the backdrop closes it (repo rule: modal dialogs handle Escape and click-outside dismissals).
- The setup composer's API key dropdown listed keys by name only, so several keys sharing one name looked like duplicate entries; options now also carry the key's masked display value to disambiguate them.
- Setup instruction steps hardcoded the literal `<your-api-key>` placeholder even when the one-command setup and environment blocks above them already embedded the selected key; rendered steps now substitute the same selected key, and keep the placeholder only when no key exists.
- Page heading duplicated the top bar ("CLI Tools" above "CLI & IDE Tools"); the in-page heading now matches the section name.
- Card type labels rendered with broken casing ("Cli Tool", "Ide Tool", "Mitm Tool") from a CSS capitalize over the raw category id; they now use explicit labels (CLI Tool / IDE Tool / MITM Tool).
- Tool card logos left blank dark tiles for seconds on first paint because the letter fallback only appeared on image error while the image lazy-loaded; the colored initial tile now paints immediately underneath and the logo fades in over it once loaded.
- Tool descriptions that read as raw config paths (Pi, Oh My Pi, Crush, Forge, Smelt, CodeWhale) or repeated the tool's own name (OpenDesign) are rewritten as plain descriptions, matching the tone of the rest of the catalog.
