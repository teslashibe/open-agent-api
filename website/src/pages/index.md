---
title: Codex Chat API Operator Guide
description: Practical deployment and operations guidance for codex-chat-api.
hide_table_of_contents: true
---

# Codex Chat API Operator Guide

Use this guide to configure redacted credentials, operate a multi-client pool,
validate dev, and promote a tested image to production.

The delivery contract is deliberately small:

- A push to `main` builds an immutable `sha-<short>` image and updates **dev**.
- A `v*` tag builds the matching version image and updates **prod**.
- Pull requests build this site so broken operator instructions block merging.

[Start with gateway setup](./docs/setup) or review the
[production-readiness runbook](./docs/production-readiness) and the
[pipeline and DAG contract](./docs/pipeline-dag).

All examples use local addresses, safe aliases, and placeholders. Never copy an
OAuth token, bearer secret, account identifier, or internal hostname into the
repository, a client label, a command transcript, or an issue.
