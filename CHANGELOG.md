# Changelog

## [0.8.4](https://github.com/loopingz/escrow-proxy/compare/v0.8.3...v0.8.4) (2026-06-25)


### Bug Fixes

* cache revalidation with stale fallback + OCI digest verification ([793bcc2](https://github.com/loopingz/escrow-proxy/commit/793bcc2b0853060dba2a41f172e6c60dc2779de7))
* pin Content-Length and drop chunked framing on buffered responses ([#24](https://github.com/loopingz/escrow-proxy/issues/24)) ([ab874dd](https://github.com/loopingz/escrow-proxy/commit/ab874ddeed4557316b2403ab20a38b89fe02c434))

## [0.8.3](https://github.com/loopingz/escrow-proxy/compare/v0.8.2...v0.8.3) (2026-06-11)


### Bug Fixes

* update ([c056069](https://github.com/loopingz/escrow-proxy/commit/c05606947243a4d220b3fabb4dfff17b6946070a))

## [0.8.2](https://github.com/loopingz/escrow-proxy/compare/v0.8.1...v0.8.2) (2026-05-13)


### Bug Fixes

* head should not compute digest ([392f714](https://github.com/loopingz/escrow-proxy/commit/392f714c4ee960513255494528085bbcb7b55c47))

## [0.8.1](https://github.com/loopingz/escrow-proxy/compare/v0.8.0...v0.8.1) (2026-05-13)


### Bug Fixes

* build and add ci checks ([d9de6bc](https://github.com/loopingz/escrow-proxy/commit/d9de6bcf22a4fe261f6ab1278079995677e9f054))
* go mod tidy ([95bbe21](https://github.com/loopingz/escrow-proxy/commit/95bbe21494353ce68d51ff2492331a4cc505ccf0))

## [0.8.0](https://github.com/loopingz/escrow-proxy/compare/v0.7.1...v0.8.0) (2026-05-13)


### Features

* add sha256 verification ([2667821](https://github.com/loopingz/escrow-proxy/commit/2667821897b5a0810967249f0107cb5741844f74))

## [0.7.1](https://github.com/loopingz/escrow-proxy/compare/v0.7.0...v0.7.1) (2026-05-12)


### Bug Fixes

* expire certificates ([bdac8f3](https://github.com/loopingz/escrow-proxy/commit/bdac8f3847344a7ee17f0dad002801a105eb8077))
* invalid request handling ([d4ade9c](https://github.com/loopingz/escrow-proxy/commit/d4ade9c417dbd0002c75d49624f1840a4397caf1))
* rebuild index as a background task ([11a8354](https://github.com/loopingz/escrow-proxy/commit/11a8354a2ba92d295b55e1f7ae1891d9c68b5e19))

## [0.7.0](https://github.com/loopingz/escrow-proxy/compare/v0.6.0...v0.7.0) (2026-05-06)


### Features

* **proxy:** follow upstream redirects, cache terminal body ([#12](https://github.com/loopingz/escrow-proxy/issues/12)) ([55aa20d](https://github.com/loopingz/escrow-proxy/commit/55aa20dcf67022f7006b4b2351ad8dda0f642a6a))

## [0.6.0](https://github.com/loopingz/escrow-proxy/compare/v0.5.0...v0.6.0) (2026-05-04)


### Features

* **cli:** read --config and --log-level from env vars ([#10](https://github.com/loopingz/escrow-proxy/issues/10)) ([93a1d0c](https://github.com/loopingz/escrow-proxy/commit/93a1d0c2ad0fd9944847df91d3836b5b556d304d))

## [0.5.0](https://github.com/loopingz/escrow-proxy/compare/v0.4.0...v0.5.0) (2026-04-30)


### Features

* **cache:** add SQLite index + cache evict/reindex CLI ([#9](https://github.com/loopingz/escrow-proxy/issues/9)) ([0c07abc](https://github.com/loopingz/escrow-proxy/commit/0c07abc452020a3bd132a3834292e3c801a86f06))
* **cli:** add cache list and cache show subcommands ([#7](https://github.com/loopingz/escrow-proxy/issues/7)) ([7ebfb78](https://github.com/loopingz/escrow-proxy/commit/7ebfb787ecb9d81dc1d9b7f1e3490a5db5e657f0))

## [0.4.0](https://github.com/loopingz/escrow-proxy/compare/v0.3.0...v0.4.0) (2026-04-26)


### Features

* **cli:** add cache invalidate subcommand ([#5](https://github.com/loopingz/escrow-proxy/issues/5)) ([a428ecd](https://github.com/loopingz/escrow-proxy/commit/a428ecd1845e204ba9a6f2fe15058ca5452aecf9))

## [0.3.0](https://github.com/loopingz/escrow-proxy/compare/v0.2.1...v0.3.0) (2026-04-20)


### Features

* **cache:** restrict caching to GET/HEAD by default and add URL excludes ([#3](https://github.com/loopingz/escrow-proxy/issues/3)) ([0e3c609](https://github.com/loopingz/escrow-proxy/commit/0e3c60997b46e023e787fbbab5b674f6fcb5e8c6))

## [0.2.1](https://github.com/loopingz/escrow-proxy/compare/v0.2.0...v0.2.1) (2026-04-20)


### Bug Fixes

* **ci:** chain goreleaser in release-please workflow via conditional job ([0c3f4ed](https://github.com/loopingz/escrow-proxy/commit/0c3f4edf1be13ada3110ef0f015bf8d71eb9da9d))

## [0.2.0](https://github.com/loopingz/escrow-proxy/compare/v0.1.0...v0.2.0) (2026-04-20)


### Features

* add archive format auto-detection from destination path ([9a255a6](https://github.com/loopingz/escrow-proxy/commit/9a255a68ef3962ba61f35dab6c92b952581ae7a7))
* add archive interfaces and tar.gz format implementation ([854e518](https://github.com/loopingz/escrow-proxy/commit/854e518f614eac3b6c655cb9fcd2d487a6c2e6e0))
* add cache entry types and cache key computation ([f761d8e](https://github.com/loopingz/escrow-proxy/commit/f761d8ead8185a4cfddd6b87c4a43fe1e9fbe806))
* add cache layer with meta/body storage ([93dcfa0](https://github.com/loopingz/escrow-proxy/commit/93dcfa07bc7ab082bb961fcdbcca13d8f52b01c8))
* add cobra CLI entrypoint with all subcommands ([76e2513](https://github.com/loopingz/escrow-proxy/commit/76e25137ab8227d5e9e5b4b8bd5cd4369ff278e0))
* add config loading tests ([f1a829c](https://github.com/loopingz/escrow-proxy/commit/f1a829c85c44044c5fbab185967c4cc23b18c169))
* add custom CAS archive format with content deduplication ([d394d1a](https://github.com/loopingz/escrow-proxy/commit/d394d1a8033972849aecf30aaea108799b40ecc2))
* add distroless Dockerfile for release images ([4c2701c](https://github.com/loopingz/escrow-proxy/commit/4c2701c99dd64e8ca0d1a017764eaafc35ccc262))
* add GCS and S3 storage backends ([844231b](https://github.com/loopingz/escrow-proxy/commit/844231b3dbd84b61194f74d6df25396951c1d6c9))
* add GoReleaser config for multi-arch release builds ([636969c](https://github.com/loopingz/escrow-proxy/commit/636969ce16860eda02393e489589ba88a3bc1441))
* add MITM proxy engine with cache-aware request handler ([957b68a](https://github.com/loopingz/escrow-proxy/commit/957b68a77642f0960ff5366290f561cdaac42b21))
* add OCI archive format with layer grouping and registry push/pull ([77fa0c4](https://github.com/loopingz/escrow-proxy/commit/77fa0c4b9d19c62f8e29765543559c7bdb480cf4))
* add proxy integration tests for serve, record, and offline modes ([39dd8fc](https://github.com/loopingz/escrow-proxy/commit/39dd8fc497cc00acf8931d8c0b40f047db80df27))
* add release-please config for conventional-commit releases ([3f753ad](https://github.com/loopingz/escrow-proxy/commit/3f753ad8fd9456e5618d4ca8ca1b4d3f1e17553b))
* add storage interface and local filesystem backend ([8baed53](https://github.com/loopingz/escrow-proxy/commit/8baed5338ef469dccca2bd29fac33e7c5aca68b9))
* add tiered storage multiplexer with L1 promotion ([9e1184f](https://github.com/loopingz/escrow-proxy/commit/9e1184f3a3087119f636774c937cca679aa9a4a1))
* add TLS CA generation, leaf cert cache, and PEM export ([074b597](https://github.com/loopingz/escrow-proxy/commit/074b597631a961ba258fdb4363be76681900e23c))
* **cli:** add buildVersion helper for --version output ([52583c2](https://github.com/loopingz/escrow-proxy/commit/52583c24d497ca370ea6bce89912d3e55e68ccd2))
* **cli:** expose --version with build metadata ([5f65ec9](https://github.com/loopingz/escrow-proxy/commit/5f65ec9c04a0cd0fdf43af4ad46a00828bd04055))
* **release:** sign SBOMs with cosign keyless ([6f29dfe](https://github.com/loopingz/escrow-proxy/commit/6f29dfe034d1a38db4458d55330992f40efeab30))
* scaffold project with cobra CLI and config parsing ([e3eff30](https://github.com/loopingz/escrow-proxy/commit/e3eff30234dd33395f9d9010a747401771d9e9e9))
* wire up CLI subcommands with proxy, cache, storage, and archive layers ([f08f97c](https://github.com/loopingz/escrow-proxy/commit/f08f97c429856dc9cef7d33b44e4a1ccb37b165f))


### Bug Fixes

* anchor escrow-proxy gitignore pattern to root ([90b7c56](https://github.com/loopingz/escrow-proxy/commit/90b7c563d155e1a3bc7664efd2b2d5705932ea0b))
* proxy management ([805f188](https://github.com/loopingz/escrow-proxy/commit/805f1882b4f481faed78286fd5ed4fd8f07d3f76))


### Documentation

* add markdown ([824ea89](https://github.com/loopingz/escrow-proxy/commit/824ea893c752b5167fc6e659ec325b93344486b3))
* add release automation design spec ([3ba9c1c](https://github.com/loopingz/escrow-proxy/commit/3ba9c1cfec3114494144b8dc09174c3ab2d0bde9))
* add release automation implementation plan ([e7aed65](https://github.com/loopingz/escrow-proxy/commit/e7aed65d3db7cc4e5e52380132cb2f24707f55b9))
* document container image and cosign verification ([6c66e6f](https://github.com/loopingz/escrow-proxy/commit/6c66e6f05fe3eda24d22b9a0aef0b69335913d36))
* document SBOM signature verification ([a0a99e2](https://github.com/loopingz/escrow-proxy/commit/a0a99e24a5e5fa8e4116d955d98a0da8b09a1ba3))
