# Changelog

## [0.4.0](https://github.com/bluefunda/btp-go/compare/v0.3.0...v0.4.0) (2026-05-11)


### Features

* **httpclient:** HTTP CONNECT proxy transport for BTP on-prem destinations ([#8](https://github.com/bluefunda/btp-go/issues/8)) ([33bab37](https://github.com/bluefunda/btp-go/commit/33bab3740ca373bda4e256f8eb815b60ff717b24))


### Bug Fixes

* **examples/http-odata:** update to httpclient v0.1.1 API ([#10](https://github.com/bluefunda/btp-go/issues/10)) ([eaf03b5](https://github.com/bluefunda/btp-go/commit/eaf03b58e981d1412341c982d65b7713058b7c34))

## [0.3.0](https://github.com/bluefunda/btp-go/compare/v0.2.1...v0.3.0) (2026-05-10)


### Features

* **examples:** add http-odata and sftp-sshclient example apps ([#6](https://github.com/bluefunda/btp-go/issues/6)) ([bc1e3c8](https://github.com/bluefunda/btp-go/commit/bc1e3c84f27f0239073f84161b5cfc425c666a5e))


### Bug Fixes

* bump go directive to 1.25.10 to resolve govulncheck stdlib CVEs ([#3](https://github.com/bluefunda/btp-go/issues/3)) ([a7b7832](https://github.com/bluefunda/btp-go/commit/a7b7832449cdfb5e6d8b779fdaed92a7bc4d7932))
* **ci:** trigger CI on release-please branches ([#7](https://github.com/bluefunda/btp-go/issues/7)) ([d9be261](https://github.com/bluefunda/btp-go/commit/d9be2610532ad5ca18106048c2e6acf2421e6333))

## [0.2.1](https://github.com/bluefunda/btp-go/compare/v0.2.0...v0.2.1) (2026-05-07)


### Bug Fixes

* **examples/sftp-count:** pin btp-go deps to released semver tags ([059a02e](https://github.com/bluefunda/btp-go/commit/059a02e9e8b3fe735f2a1b02c256337604ff8c46))

## [0.2.0](https://github.com/bluefunda/btp-go/compare/v0.1.2...v0.2.0) (2026-05-07)


### Features

* **binding/kyma:** Kyma/Kubernetes file-mounted binding provider ([793262b](https://github.com/bluefunda/btp-go/commit/793262b191583bfbc3016f86b68ff3fb08667940))
* Dialer interface in sshclient; Finder, BestAuthToken, ListAll in destination ([05f9a90](https://github.com/bluefunda/btp-go/commit/05f9a902edde254974a86fa9e0db9d24797cd4eb))
* **httpclient:** HTTP client for BTP HTTP destinations (feat/m3-httpclient) ([4cb159a](https://github.com/bluefunda/btp-go/commit/4cb159ac7f4203269abc77be5e1183f3401ad859))


### Bug Fixes

* **httpclient:** local Dialer interface; pin connectivity v0.1.2 + destination v0.2.0 ([45bd7ac](https://github.com/bluefunda/btp-go/commit/45bd7ac429007e53915d0d788234a55d4e49fe58))
