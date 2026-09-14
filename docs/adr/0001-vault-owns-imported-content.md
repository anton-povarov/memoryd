# The Vault owns imported content

When a local file is imported, memoryd creates and owns a durable copy rather than relying on the file at its original path. This makes a Memory stable when the external file is moved, changed, or deleted. Its full original path and other Import Context are retained as provenance Facts, not as live filesystem dependencies.
