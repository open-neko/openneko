# Recipes

A recipe is a service that runs beside OpenNeko. The core does not build,
ship, or start it. The core reaches it by URL.

- `embedding-gemma`: an embedding service for semantic search.
- `sandbox`: runs the agent's terminal commands, the script skills and
  workflow script steps, a user per run.
- `lazy-service`: a small listener that starts a heavy child process on the
  first request and stops it when idle. The embedding recipe uses it.
