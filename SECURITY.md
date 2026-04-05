# Security

## Scope

This project stores local WhatsApp session state and cached chat metadata on disk. Treat the configured data directory as sensitive.

## Current Protections

- app directories are created with restrictive permissions
- debug logging is opt-in
- message bodies are not logged by default
- app state and session state are isolated into separate SQLite files

## Known Risk

This is not an official WhatsApp client. Even when the code and dependency graph are clean, unofficial-client/account risk remains. That includes the possibility of service-side policy enforcement outside the control of this repository.

## Reporting

Do not open public issues for security-sensitive reports. Send a private report to the maintainer with:

- affected version or commit
- impact summary
- reproduction steps
- suggested mitigation if available
