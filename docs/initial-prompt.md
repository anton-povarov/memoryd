THIS FILE EXISTS FOR HISTORICAL REFERENCE ONLY.

› I want to build a personal Memory Vault app, you would drop files into it and it would understand the content of those files and
  then let you semantically search for them using natural language based on file metadata and content.
  The idea in plain terms is - i'm sick of forgetting where i put information (like a bill, or a passport photo, an important email,
  or a link to a great article, etc.), so i want a smart program to help me find it.
  So i'd query it like "hey, please find me all tasleem bills for this year" or "find me my wife's passport photo", etc.

  I call it memoryd.

  Feature dreams
  - memoryd should be accessible from multiple devies, so i guess we'll need a client-server architecture.
  - I want memoryd to be searchable in natural language, but also with metadata - like datetime created or location or filetype.
  - I want to have multiple client types (web/mobile/native macos app) and also integrations (openclaw, mcp, obsidian).
  - Parsers / "understanders" for various file/content types (i.e. parse a DEWA bill correctly, extract passport photo details, etc.)
  - not even based on file type, but content of the file as well. Pluggable, adding more over time.

  To start - let's be modest and smart very small - i.e. no big distributed systems yet, just this laptop. A couple file formats it
  would understand. Basic natural language query capability. Basic web UI.

  Let's get the basics discussed, using $grill-with-docs skill.
