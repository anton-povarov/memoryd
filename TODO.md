# TODO

## Document Understanding
- Redesign how Understanding Runs are represented in memory metadata.
  As new plugins are added, we need to be able to add their artefacts to active undertanding runs.
  It feels that the concept of understansing runs needs a revision,
  i.e. runs are an operational concept, artefacts are stored/updated by plugins.
- Plugin configuration
  - multiple plugins to run per media type
  - [?] allow plugins to declare media types they support (on startup?). 
  	This is tricky as long as the plugin model is cgi-bin-like.
	So maybe we need pattern matching in the config per media type.
	And a */* plugin that would use a harness to describe the file very generically (maybe via a skill).
- Define resource limits for Document Understanding, especially that now we have plugins using external models.

## Vault
- Act on the "Understanding is rebuildable" ADR. `docs/adr/0003-understanding-is-rebuildable.md`.
  Pair it with the research document `docs/future/storage-authority-options.md`.