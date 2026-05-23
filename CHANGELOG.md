# Changelog

## [1.13.0](https://github.com/open-neko/neko/compare/v1.12.1...v1.13.0) (2026-05-23)


### Features

* source_change subscriptions for IFTTT-style data-source row triggers ([#50](https://github.com/open-neko/neko/issues/50)) ([dc0e3df](https://github.com/open-neko/neko/commit/dc0e3dfc9f254d87bec1ed01b150c3261985d058))

## [1.12.1](https://github.com/open-neko/neko/compare/v1.12.0...v1.12.1) (2026-05-22)


### Bug Fixes

* **demo:** wire trial sim + scenario injector into packaged demo.yml ([#47](https://github.com/open-neko/neko/issues/47)) ([e0deef4](https://github.com/open-neko/neko/commit/e0deef478f88f4ac9443d61f78349321a8dced09))

## [1.12.0](https://github.com/open-neko/neko/compare/v1.11.4...v1.12.0) (2026-05-21)


### Features

* connect capability, install policy, /integrations, M6+M7+M8+M9+M10+M11 ([#42](https://github.com/open-neko/neko/issues/42)) ([a12124e](https://github.com/open-neko/neko/commit/a12124e253021769cb8dd5eb263273c65b476b3e))

## [1.11.4](https://github.com/open-neko/neko/compare/v1.11.3...v1.11.4) (2026-05-21)


### Bug Fixes

* **deploy:** write slack secret before install, not after ([5674f9b](https://github.com/open-neko/neko/commit/5674f9b3977d814624c386b75fe8c417d0236c15))

## [1.11.3](https://github.com/open-neko/neko/compare/v1.11.2...v1.11.3) (2026-05-21)


### Bug Fixes

* **deploy:** split privileged setup into a one-time on-VM script ([e0c2a02](https://github.com/open-neko/neko/commit/e0c2a020dcb31e64973f48910b832f209001f57a))

## [1.11.2](https://github.com/open-neko/neko/compare/v1.11.1...v1.11.2) (2026-05-21)


### Bug Fixes

* **header:** hide brand chip on mobile so it stops covering the menu ([cf95873](https://github.com/open-neko/neko/commit/cf958739f884030b825aa90cc3fa1c418e3b7502))

## [1.11.1](https://github.com/open-neko/neko/compare/v1.11.0...v1.11.1) (2026-05-21)


### Bug Fixes

* **deploy:** unbreak heredoc + install openneko + auto-install slack plugin ([7f94c67](https://github.com/open-neko/neko/commit/7f94c675bf2cf3866af91e11457d6108b2abb221))

## [1.11.0](https://github.com/open-neko/neko/compare/v1.10.0...v1.11.0) (2026-05-21)


### Features

* **plugins:** poll-fallback watcher + host-check warn + neko-vm plugin dir ([a5a8ffa](https://github.com/open-neko/neko/commit/a5a8ffab694fbd83248f49b49f1a397f01038091))

## [1.10.0](https://github.com/open-neko/neko/compare/v1.9.0...v1.10.0) (2026-05-20)


### Features

* **plugins:** host-to-worker install proxy + isolated plugin dir ([417583d](https://github.com/open-neko/neko/commit/417583d3f108c9d699e607f65fd29e578e289cfb))

## [1.9.0](https://github.com/open-neko/neko/compare/v1.8.0...v1.9.0) (2026-05-20)


### Features

* **cli:** host-to-worker plugin-op proxy + chown /app ([2c7e574](https://github.com/open-neko/neko/commit/2c7e574065cf4642444e4e941d6952c33aac0c28))

## [1.8.0](https://github.com/open-neko/neko/compare/v1.7.5...v1.8.0) (2026-05-20)


### Features

* **supervisor:** name compose project openneko-&lt;mode&gt; instead of "runtime" ([396f129](https://github.com/open-neko/neko/commit/396f12917d60bb42e3322242f0bd62a3405b8582))

## [1.7.5](https://github.com/open-neko/neko/compare/v1.7.4...v1.7.5) (2026-05-20)


### Bug Fixes

* **demo:** add data-source graphjin + config-init to embedded demo.yml ([3cca884](https://github.com/open-neko/neko/commit/3cca8845842b2dfa99110277ed070ae8f6f57aaf))

## [1.7.4](https://github.com/open-neko/neko/compare/v1.7.3...v1.7.4) (2026-05-20)


### Bug Fixes

* **docker:** copy onnxruntime-node native libs into web image ([9ccda7d](https://github.com/open-neko/neko/commit/9ccda7d0b3677f4abcf1ca1c7b3273a263091750))

## [1.7.3](https://github.com/open-neko/neko/compare/v1.7.2...v1.7.3) (2026-05-20)


### Bug Fixes

* **docker:** copy plugin-install + plugin-types node_modules into worker ([96aae00](https://github.com/open-neko/neko/commit/96aae00bc62c163d50a757add909403360409655))

## [1.7.2](https://github.com/open-neko/neko/compare/v1.7.1...v1.7.2) (2026-05-20)


### Bug Fixes

* **docker:** bump go-build to golang:1.25-bookworm + pre-create /cache ([3083277](https://github.com/open-neko/neko/commit/30832779e2ec655478246f39fc514a037665ee28))

## [1.7.1](https://github.com/open-neko/neko/compare/v1.7.0...v1.7.1) (2026-05-20)


### Bug Fixes

* **docker,demo:** self-contained worker image for openneko --mode demo ([5da2214](https://github.com/open-neko/neko/commit/5da2214d29e0e79633318d73aecc352f6b12efbf))

## [1.7.0](https://github.com/open-neko/neko/compare/v1.6.0...v1.7.0) (2026-05-19)


### Features

* SSO via plugin runtime + plugin action tools in /work ([#24](https://github.com/open-neko/neko/issues/24)) ([ccaee8d](https://github.com/open-neko/neko/commit/ccaee8d7ed5b7e5dc5884b88f4fcdd4025cb9369))

## [1.6.0](https://github.com/open-neko/neko/compare/v1.5.0...v1.6.0) (2026-05-19)


### Features

* **auth,web,worker:** SSO via the plugin runtime + Scalekit-ready sign-in ([#22](https://github.com/open-neko/neko/issues/22)) ([0f5ee16](https://github.com/open-neko/neko/commit/0f5ee16003e625b6837bf6bb61dd7b8392849f28))

## [1.5.0](https://github.com/open-neko/neko/compare/v1.4.0...v1.5.0) (2026-05-17)


### Features

* **plugins:** sandboxed plugin system + hot-reload registry + vendored CLI ([#20](https://github.com/open-neko/neko/issues/20)) ([1c41d30](https://github.com/open-neko/neko/commit/1c41d30b4e702103ccb7137b89bb7455676ffbea))

## [1.4.0](https://github.com/open-neko/neko/compare/v1.3.0...v1.4.0) (2026-05-17)


### Features

* **memory,ui:** tighten workflow+metric memory; rename /settings/policies → /settings/rules ([3b012db](https://github.com/open-neko/neko/commit/3b012db370294e11bae17ee02a71687fe2b27e7e))

## [1.3.0](https://github.com/open-neko/neko/compare/v1.2.0...v1.3.0) (2026-05-17)


### Features

* **ui:** grouped Auto-response cards, three-act dashboard, rule-save chips ([29964fe](https://github.com/open-neko/neko/commit/29964fec3d087ed902938ae8bfa9faa46dbd08ac))
* **ui:** make rule/workflow/action event chips clickable ([4e4a6ef](https://github.com/open-neko/neko/commit/4e4a6ef96abcba249ee4444e3092ab56c7a1d3f2))
* **ui:** WorkflowSavedCard + ActionRequestCard chips for chat fences ([50501fe](https://github.com/open-neko/neko/commit/50501fe8c9408ae224da8e5863730aa8fd009cc9))


### Bug Fixes

* **work:** strip neko_policy_save fence + show RuleSavedCard in /work ([6d9a0a4](https://github.com/open-neko/neko/commit/6d9a0a4c58ca1c1ef40b6740ece04f2b82d38a05))

## [1.2.0](https://github.com/open-neko/neko/compare/v1.1.0...v1.2.0) (2026-05-15)


### Features

* **actions:** action receipts — close the loop on auto-fired actions ([#15](https://github.com/open-neko/neko/issues/15)) ([fee7860](https://github.com/open-neko/neko/commit/fee7860fa4eaecee2a511dd9fe650c65faa1583d))


### Bug Fixes

* **work,metric:** surface get_table_sample and syntax patterns to agents ([c9fff9c](https://github.com/open-neko/neko/commit/c9fff9c9bb69d9b6eb962410dbbacf622302fe02))
* **work,ux:** editorial /ask facelift ([ae70b45](https://github.com/open-neko/neko/commit/ae70b450c137578dffbb73604f3c2afac05cb3d5))
* **work,ux:** flatten /work panel and float the composer ([d8247a3](https://github.com/open-neko/neko/commit/d8247a37e4c521234187eb433da5f99ed16528bf))
* **work,ux:** harmonise /ask empty state with the conversation state ([3489905](https://github.com/open-neko/neko/commit/3489905be6df28d265bbf8434ac7e8087fec24cd))
* **work,ux:** hide workflow threads from Ask sidebar; widen header band ([9e5756e](https://github.com/open-neko/neko/commit/9e5756e8e15c89b66e76d2e3bc9572867f035ac7))
* **work,ux:** refine /work composer for desktop and touch ([5d1cb61](https://github.com/open-neko/neko/commit/5d1cb61098f5868e1fac1c29c198e275d57bd45d))
* **work,ux:** share shell layout across /work, /skills, /memory ([4a91daa](https://github.com/open-neko/neko/commit/4a91daab9705b4aa5fff581ef15d68e8960256ec))
* **work,workflows:** add worked GraphJin aggregation examples to prompt ([d7cad7b](https://github.com/open-neko/neko/commit/d7cad7b170ae771dfe1305c4eda27a6f1f49e57e))
* **work,workflows:** inline syntax.json into Work + workflow runner prompts ([7a9b1f3](https://github.com/open-neko/neko/commit/7a9b1f3759aef82b1c4efe8636ff8ec6ec34bbf3))
* **work,workflows:** stop inlining 77KB of knowledge into ACP prompts ([fdd722f](https://github.com/open-neko/neko/commit/fdd722faafe5b352ca7ded7b026a589005a8b781))
* **work:** make attachments actually reach the agent ([b09370b](https://github.com/open-neko/neko/commit/b09370b5ceeda8140aa472b8cec095e230a32985))
* **work:** title deep-dive threads from the briefing card metric ([e63d043](https://github.com/open-neko/neko/commit/e63d043b0a4738d8c5541d66aecf99b6463342aa))

## [1.1.0](https://github.com/open-neko/neko/compare/v1.0.0...v1.1.0) (2026-05-14)


### Features

* **trial-sim:** L3 scripted scenarios via sidecar injector ([dfdadc4](https://github.com/open-neko/neko/commit/dfdadc40a5848d47a1d63d7248821cbef7162b6b))


### Bug Fixes

* **work,workflows:** bake GraphJin knowledge into agent prompts ([074ea48](https://github.com/open-neko/neko/commit/074ea48ff3c3fca620e130c1211df36af51e1a06))
* **work:** restore /work layout width collapsed by mobile pass ([bbbcafd](https://github.com/open-neko/neko/commit/bbbcafd3088015ebfcca73c009393827e82397ea))
