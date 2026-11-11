---
type: fixed
---

The CLI Tools configuration modal could not be scrolled on mobile at all, and the Hermes manual-configuration modal overflowed off-screen the same way. Root cause: both are flex-column shells whose scroll region lacked min-h-0, so the region refused to shrink below its content height and the shell's overflow-hidden clipped everything past the viewport — on desktop the content happened to fit, on a phone it did not. Both modals are redesigned as mobile-first sheets: full-height bottom-anchored sheets on small screens (rounded top, safe-area-aware footer padding, overscroll containment so a scroll never chains to the page behind), the familiar centered card on larger screens, a real 32px close button, and a pinned header and footer around the now-actually-scrollable body. The Hermes modal additionally gains the height cap and scroll region it never had, and long environment-variable lines in the tool modal wrap instead of pushing the layout sideways.
