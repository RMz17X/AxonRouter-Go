# Fix request-details list 500 from 1101 (1102)

1101 added the smartDecision column to the scan and to the
requestDetailListSelect constant, but GetRequestDetailsPaged runs its
own inline copy of the column list (the collapsed DISTINCT ON query) —
that copy missed the new column, so the scan failed and the list
endpoint returned 500. The inline query now extracts the same
smartDecision sub-document, in the same trailing position.
