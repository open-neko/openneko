# Changelog

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
