# Understanding Plugins are external processes

Specialized enrichment plugins run behind a small process protocol rather than inside memoryd's application process, this keeps plugins language-independent, allows them to integrate without coupling to memoryd internals and fail without affecting memoryd.
A versioned manifest from a plugin declares it's capabilities.
Memoryd and plugins communicate over a stream of structured messages and events (other ADRs will define the protocol when ready).