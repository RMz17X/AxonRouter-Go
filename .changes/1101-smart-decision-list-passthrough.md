# Smart decision in the request-details list (1101)

Follow-up to 1100: the decision was recorded in the stored payload and
shown in the detail modal, but the request-details LIST projection did
not carry it, so the list-row chip never rendered. The list select now
extracts the `smartDecision` sub-document alongside the caller fields,
the list row carries it verbatim, and the list item passes it through —
the chip shows in the table without opening the detail.
