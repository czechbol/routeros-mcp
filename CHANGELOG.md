# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Surface spec source (live vs bundled) in `ros_describe` output

### Changed
- Drop ineffective `isDestructive` gate in REST mutator path
- Stream live OpenAPI spec from disk to cut idle RSS

### Security
- Scrub credentials from error bodies and string responses
- Require `Origin` header when allowlist configured
- Validate and escape REST path segments

## [0.1.0] - 2026-05-16

### Added
- Initial RouterOS MCP server implementation [86068be](https://github.com/czechbol/routeros-mcp/commit/86068be)
