# Memory identity is content-based

For the MVP, byte content determines Memory identity and storage location: the first import of a unique content hash creates a Memory in content-addressed storage, and later imports of the same bytes are rejected as Duplicates. This prevents repeated directory imports from multiplying results; memoryd deliberately does not merge new Import Context from rejected imports yet.
