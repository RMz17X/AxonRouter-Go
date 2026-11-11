### Fixed

- Docker BuildKit cache in the CI and Release workflows is now written only from `main` runs (`cache-to` is empty on pull requests and release tags). Tag-scoped BuildKit blobs were write-once-never-read and had piled up to ±7.7 GB of the account's Actions storage, pushing it past the 2 GB included quota into paid overage; the workflows still read the shared cache from `main`, so build speed is unchanged where it matters.
