# Media Type resolution is handled by the Server with fallback to client declarations

The Vault accepts any Blob within its size limit; content-addressed Blob storage is indifferent to format. During Memory import, the server detects Media Type from bytes with a content classifier and uses the multipart file part's `Content-Type` only when detection is generic, so unknown formats remain durable without a fixed format list in the HTTP contract. The stored Media Type is returned on download, while extractors may support a narrower set of formats.
