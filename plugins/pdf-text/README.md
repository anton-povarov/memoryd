# PDF Markdown Understanding Plugin

`mem-understand-pdf` implements the [v2 Understanding Plugin protocol](../../docs/understanding-plugin-v2.md). It uses PyMuPDF4LLM to extract Markdown from an in-memory PDF and returns one `text/markdown` artifact. The plugin marks page boundaries, retains the library's headings and tables, and reports the extraction method and library versions in artifact provenance. Pages without extractable text produce warnings. OCR is disabled for this first plugin.

The plugin is a standalone [uv project](pyproject.toml) with a committed `uv.lock`. Run it through the diagnostic command from the repository root:

```sh
go run ./server/cmd/mem-understand /path/to/input.pdf --plugin="$(pwd)/plugins/pdf-text/mem-understand-pdf"
```

The executable wrapper uses `uv run --locked`, which creates and syncs the environment on first use. No separate `uv sync` or `uv lock` command is needed. The plugin receives PDF bytes in the JSON request and never needs a Vault path. The executable writes only the JSON result to stdout; diagnostics go to stderr.
