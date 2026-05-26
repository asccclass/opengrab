# Memory Schema

This project follows the LLM Wiki pattern:

- `memory/raw/` stores immutable source material.
- `memory/wiki/` stores LLM-maintained markdown synthesis.
- `memory/wiki/index.md` catalogs wiki pages and should be read first.
- `memory/wiki/log.md` is append-only chronological history.

When answering, use the wiki context as durable memory. When new durable information appears in conversation, save it as a page, update the index, and append to the log.
