# Go
1. Avoid magic numbers/strings in code, make them constants unless local and completely obvious.

# Vault
1. Always use struct Blobref to represent Blobref-s, never concat strings.

# Logging
1. Server uses structured logging, the package is in `server/internal/logging`.
2. Ensure high quality logging in important processing stages and code paths.
3. Ensure that any top level errors or errors preventing progress of a request are logged.
