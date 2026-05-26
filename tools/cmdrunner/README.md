# cmdrunner

`cmdrunner` is a small Go package for letting an LLM execute commands safely and predictably.

The key design rule is:

- default to `args`
- use `shell` only when shell syntax is actually required

This package is intended for use under:

```text
opengrab/tools/cmdrunner/