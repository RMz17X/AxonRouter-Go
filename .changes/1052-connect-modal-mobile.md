### Connect modal: step rows stop overflowing on phones, buttons slim to 32px, one-tap Paste for the code

- Owner's phone report (photo of the Connect Antigravity modal):
  Step 1's single row — URL field, Open login page, Copy — does not
  fit a phone: the Copy button was pushed past the modal's edge and
  the Open button's label wrapped into a three-line stack, ballooning
  its height. Step 1 now stacks on small screens: the URL field takes
  its own full-width row (min-w-0 so it can actually shrink), and the
  two actions share an equal-halves row below it; on sm+ the wrapper
  dissolves (sm:contents) and the three sit on one row again. All
  labels are whitespace-nowrap.
- Button heights in the modal are fixed h-8 (32px) instead of
  padding-sized: Step 1's Open/Copy, the new Paste button, Connect /
  Cancel in the footer, and the Kiro social row's Open (whose URL
  field gets the same min-w-0 treatment). The modal shell also trades
  p-6 for p-4 on phones (sm:p-6), returning width to the content.
- Step 2 gains a Paste button beside the code field: one tap reads
  the clipboard (navigator.clipboard.readText) and fills the field,
  trimmed. Clipboard reads need a secure context and permission, so
  an empty clipboard or a refused read leaves a one-line hint under
  the field instead of failing silently — the manual long-press
  paste keeps working everywhere.
- The Step 1 / Step 2 block is shared by every OAuth connect flow
  (Antigravity, Gemini CLI, Codex, Cline, Freebuff, Kiro social), so
  the redesign lands for all of them at once.
