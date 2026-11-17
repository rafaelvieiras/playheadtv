# Security Policy

Playhead TV is a self-hosted app: a Go backend with an admin web UI and MCP
tools, a Flutter client that runs on a desktop, a phone or a television, and a
plugin host that starts an external program on the backend. Reports are
welcome.

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Report privately through GitHub's [private vulnerability reporting][gh-report]
(the **Security** tab, then **Report a vulnerability**), or by e-mail to
**hello@rafaelvieiras.com**. Include the version (`GET /version` for the
backend, the release name for the client), what an attacker gains, and the
steps to reproduce. Do not paste a real credential, an API key or your whole
database.

This is a small project: expect an answer, not a service-level agreement. You
will be credited in the release notes unless you ask not to be.

[gh-report]: https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability

## What is in scope

- The backend: REST API, MCP server, admin web UI, sessions and device tokens,
  the EPG materializer, and credentials encrypted at rest.
- The client: the update mechanism (a signed checksums file, verified against
  the keys pinned in the app), the offline cache and the pairing flow.
- The plugin host: verifying an archive, running a plugin process, and the URL
  the backend rewrites before handing it to a client.
- Anything that lets one deployment read or write another deployment's data.

## What is by design

- **One tenant.** There is no per-user permission model yet: an administrator
  can do everything, and any device that holds a device token reads playback
  data and writes one thing, a plugin source's declared settings.
- **A plugin is an arbitrary program on the backend host.** Installing one is
  an administrator action on purpose. The host verifies the archive's hash,
  refuses entries that escape the target directory, caps extracted size and
  entry count, and kills a plugin that misses its call timeout - but a plugin
  that does harm once installed is a risk of installing third-party code, not a
  hole in the host.
- **The client opens the URLs its media source and its plugin return.** A URL
  that points at a host you do not control is a source misconfiguration.
- **No telemetry, no remote service, no account.**

## Supported versions

The latest release of the backend and of the client. Fixes ship as normal
releases, and the client updates itself from them.
