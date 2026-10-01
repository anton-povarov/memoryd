# Go
1. Always list all fields in a struct initialization.
2. Avoid magic numbers in code, make them constants.
3. Add newlines before branching statements and between logical blocks in a function.

# Vault
1. Always use struct Blobref to represent Blobref-s, never concat strings.

# Logging
1. Server uses structured logging, the package is in `server/internal/logging`.
2. Ensure high quality logging in important processing stages and code paths.
3. Ensure that any top level errors or errors preventing progress of a request are logged.
