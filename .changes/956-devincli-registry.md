## devin-cli: registry entry completes the provider's wiring

The devin-cli provider was half-wired: a registered executor
(ForwardOpenAI), model capabilities, a frontend model list and alias,
and shipped dashboard artwork — but no entry in the provider registry,
so the catalog the dashboard renders could not include it and the
provider tests failed on its absence. Upstream declares it
category "free", noAuth, hidden (its registry file is not imported
into upstream's active index, which is why the port missed it).

The registry now carries the entry in exactly that shape: Devin CLI,
alias dv, CategoryFree, NoAuth, Hidden, with its website and a factual
auth hint (it runs the local `devin` binary, which must be installed
and authenticated on the host). Hidden keeps it out of the provider
lists, matching upstream's declaration.

Also: the providers display-links test's exemption set gains `mmf` —
the owner's name-only shell left after mimo-free's removal, whose
missing display data is a recorded decision, not an omission.
