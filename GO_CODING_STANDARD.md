0. Run `golangci-lint fmt` and `golangci-lint run` for changed files/packages.
  These commands would already perform `go fmt`, `go vet` so no need to run them separately.
  Fix issues. If you need to ignore a warning with `nolint` comment, explicitly report it to the user.
1. Avoid lines longer than 100 characters.
2. Format struct initializations and function calls as multiline unless short.
3. If a struct initializer or a function call is multiline, each field / parameter should be on its own line.
4. Always list all fields in a struct initialization.
5. Avoid magic numbers in code, make them constants.
6. Add newlines before branching statements and between logical blocks in a function.
