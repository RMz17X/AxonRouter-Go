title: Seed the prompt estimate at the attempt layer so no dispatch lane reaches the translators without one
version: v1.10.49

## Fixed
- The prompt-total floor (v1.10.47/48) still missed one lane: the
  entry handlers seed the body-size prompt estimate only for
  non-Claude-native streaming requests, so a combo attempt
  dispatched through a Claude-native re-entry (a Messages-speaking
  member) arrived at the translators with no estimate, and a
  segment-counting upstream's total passed unfloored — measured
  after v1.10.48 with identical forwarded prompts closing at
  prompt_tokens 89 on the unseeded lane against 446K on the seeded
  one. tryForwardWithConnection, the one point every attempt
  crosses, now seeds the estimate from the attempt's own body when
  the context carries none (streaming, no inline base64, matching
  the entry handlers' guards). An entry-seeded estimate, computed
  on the client's original body, still wins.
