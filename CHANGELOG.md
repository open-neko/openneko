# Changelog

## [2.1.0](https://github.com/open-neko/openneko/compare/v2.0.1...v2.1.0) (2026-06-11)


### Features

* **work:** delete a workflow from an Ask thread by [@mentioning](https://github.com/mentioning) it ([#79](https://github.com/open-neko/openneko/issues/79)) ([09369ed](https://github.com/open-neko/openneko/commit/09369ede134261f6fabce2462e64c5e538e7ceed))

## [2.0.1](https://github.com/open-neko/openneko/compare/v2.0.0...v2.0.1) (2026-06-11)


### Bug Fixes

* plugin sandboxes self-heal stale-name collisions + publishable plugin-base default + credential redaction ([3236b80](https://github.com/open-neko/openneko/commit/3236b807dd193b44d7fb154b817ff435959bd598))
* **worker:** plugin sandboxes self-heal name collisions; real plugin-base default; redact credentials in errors ([75e8f4e](https://github.com/open-neko/openneko/commit/75e8f4e16e485bddde974b5553d81303c45570f8))

## [2.0.0](https://github.com/open-neko/openneko/compare/v1.23.0...v2.0.0) (2026-06-11)


### Documentation

* FEATURES.md — plain-language guide to the roadmap release wave ([98caf34](https://github.com/open-neko/openneko/commit/98caf34fc15b8f8b0a7d77f5897bb17f0f201495))

## [1.23.0](https://github.com/open-neko/openneko/compare/v1.22.0...v1.23.0) (2026-06-11)


### Features

* **work:** OL5 — chat-first config of the customer GraphJin (sources/roles/access) ([d6252f0](https://github.com/open-neko/openneko/commit/d6252f0eb13cd9c1c2b447fcc2435faa40cbaf01))

## [1.22.0](https://github.com/open-neko/openneko/compare/v1.21.0...v1.22.0) (2026-06-11)


### Features

* **SEC9:** OpenShell is the only runtime — remove microsandbox + in-process production path ([9922d08](https://github.com/open-neko/openneko/commit/9922d08149d1ac5f47965e9998891097d0e2570e))

## [1.21.0](https://github.com/open-neko/openneko/compare/v1.20.0...v1.21.0) (2026-06-11)


### Features

* **briefing:** observation-elevation cards, scope muting, stat strip (OL2/OL7/OL9) ([579d0c5](https://github.com/open-neko/openneko/commit/579d0c5d66e10d7797c69fc79bc62ccd228f7e41))
* **channels,db:** channel workspace → org mapping (CH2) ([3846dba](https://github.com/open-neko/openneko/commit/3846dba46fa10335fc006601cc77be0a02d23c01))
* **channels:** capture the inbound sender identity (CH1) ([04cc6a0](https://github.com/open-neko/openneko/commit/04cc6a09b35122e36efbfe2f400e40d3112ee53e))
* **config-vcs:** invisible auto-versioning of skills/workflows/memory (CV0) ([2c0d92a](https://github.com/open-neko/openneko/commit/2c0d92a12d432ddf4209a622b02ae84bb34fb0c8))
* **db,llm,web,worker:** per-user actor in every run — the K1 keystone ([669c335](https://github.com/open-neko/openneko/commit/669c335d27e87c903e228b55ad3df35fbb8171f8))
* **db,llm:** workflow ownership — per-layer names + lineage (CV1) ([9570878](https://github.com/open-neko/openneko/commit/9570878b3e06e999cdf1a364c504d84392391ca1))
* default OpenShell on — sandboxed agent + plugins out of the box (SEC11) ([8436d5e](https://github.com/open-neko/openneko/commit/8436d5e201bbc46bb2c09b09a8a76f3f641bcc4e))
* **graphjin:** bump to 3.18.37 (GJ1 source-mode floor) + harden subscription client ([200b5f0](https://github.com/open-neko/openneko/commit/200b5f0e6370ae9f900f8e87b1024cf55851874c))
* **llm,db,web,worker:** multi-source registry + chat-first data sources (ADM2) ([717383b](https://github.com/open-neko/openneko/commit/717383b41465c5482b71b52ec193a09db4b6bcab))
* **llm,db,web,worker:** personal config layers as user/&lt;id&gt; git refs (CV4) ([72953ea](https://github.com/open-neko/openneko/commit/72953ea46fe718f441684a77b927db9e62db347f))
* **llm,db,web:** card-level finding dedupe — "2× today" (OL8) ([fb35ba1](https://github.com/open-neko/openneko/commit/fb35ba1de5db9db25d51145ce2077cf4174b894c))
* **llm,db,web:** memory fork overlay — copy-on-write personal layers (CV2) ([2716111](https://github.com/open-neko/openneko/commit/2716111aab8066c660a96489d0db7a32658a9822))
* **llm,db,web:** tamper-resistant audit log — hash chain + SIEM export (SEC10) ([9664c9e](https://github.com/open-neko/openneko/commit/9664c9e91b7173df9a3120fa130564e473e41da7))
* **llm,db,worker:** behavioral threshold alerts (SEC7) ([8f7458c](https://github.com/open-neko/openneko/commit/8f7458cdbe72c8c19ec5fae5b3a45ebb232416ad))
* **llm,db,worker:** memory integrity hash + TTL (SEC6) ([44be289](https://github.com/open-neko/openneko/commit/44be2890b97ddf97d6e8e6770c50442d57dd57fe))
* **llm,db,worker:** watchers — condition monitors over GraphJin (OL4) ([b902a87](https://github.com/open-neko/openneko/commit/b902a87aad007bc35100b8979d961ba4f308984b))
* **llm,db:** dual-identity audit on actions and gateway calls (SEC5) ([4b6ea08](https://github.com/open-neko/openneko/commit/4b6ea08611b96a29cda819f22db6fec9229953fd))
* **llm,db:** GraphJin source mode — per-run actor tokens (GJ4 core) ([d044609](https://github.com/open-neko/openneko/commit/d044609e29367066599eae1ea6c6bc778cfac756))
* **llm,plugin-install:** discovery pathways + SecretsResolver seam (GJ3, SEC2) ([22b45c4](https://github.com/open-neko/openneko/commit/22b45c4cb6bd978214720c04041c83de6a688a86))
* **llm,web,db:** operator personas — profile-shaped agent runs (CV3) ([4b529ce](https://github.com/open-neko/openneko/commit/4b529ceb4b18d0e9c7d296076d9fcb9b61841205))
* **llm,web,worker:** can() authorization seam + approver_role enforcement (K2) ([9d6ca86](https://github.com/open-neko/openneko/commit/9d6ca86e5d040cb530df91b92d2a500a0411f7ea))
* **llm,worker,web,db:** chat-first user management (ADM1) ([6f9ad88](https://github.com/open-neko/openneko/commit/6f9ad8822ff533627bcf5854217f644d7ee4c011))
* **llm,worker,web:** typed source kinds in the registry (OL5 slice) ([8a80da2](https://github.com/open-neko/openneko/commit/8a80da2ed9c8053908183b83dbd458f466e5ab75))
* **llm,worker:** agentic knowledge layering + sources mode as the default deployment ([569d9f8](https://github.com/open-neko/openneko/commit/569d9f8cd0437c37bb9099b6b8348edd19f15726))
* **llm,worker:** chat-first audit-log viewer (ADM4) ([028082f](https://github.com/open-neko/openneko/commit/028082f50d72e2875191fa601c830312e20ec8bd))
* **llm,worker:** chat-first channel management (ADM5) ([32b2509](https://github.com/open-neko/openneko/commit/32b2509d6a800e20df51138d296fd8e510760c6a))
* **llm,worker:** chat-first plugin management (ADM3) ([d73b5ea](https://github.com/open-neko/openneko/commit/d73b5eadd55543569eb2faf81647032144d52c42))
* **llm,worker:** deployment profile dial — solo/team/org/hardened (SEC8) ([afb5c44](https://github.com/open-neko/openneko/commit/afb5c44badd04d728dc694378e3951bdd2ad2aa4))
* **llm,worker:** policy-aware GraphJin guard + sandbox data egress (GJ5, GJ6, K3) ([ece57ac](https://github.com/open-neko/openneko/commit/ece57ac5d740aa1b5ac68148c71619cbdbdabbdf))
* **llm:** conservative code actions — issues + patch artifacts (OL6) ([65b1943](https://github.com/open-neko/openneko/commit/65b1943f4564fd4f792a32a3750a4db09ce0ff85))
* **llm:** iterative validation loop for job agents (GJ2) ([793333f](https://github.com/open-neko/openneko/commit/793333f24a64660532d8d4f4956cb5dd19552a87))
* **llm:** move the A2UI catalog out of the agent prompt onto the tool (ST1) ([c7acec1](https://github.com/open-neko/openneko/commit/c7acec1d3f23a92e064c0637f2e41e6ceae8cd59))
* **llm:** one actor-auth guard for every GraphJin agent path (GJ4) ([8630172](https://github.com/open-neko/openneko/commit/8630172a38969015dabec40012e52f31a11a8ed9))
* **llm:** route workflow/rule builder MCP tools through AgentControlPlane (SEC4) ([db95dfd](https://github.com/open-neko/openneko/commit/db95dfda7e584c6e733049839745c327195c158f))
* **plugin-install,db,worker:** Infisical-backed secret residency (SEC3) ([8a09952](https://github.com/open-neko/openneko/commit/8a09952e2e0b38139351b89a08960d358ffcb515))
* **security:** encrypt secrets at rest with enc:v1 in TS + Go (SEC1) ([3afa09e](https://github.com/open-neko/openneko/commit/3afa09e85ccbb2e03b6c07165a871ea422576a76))
* **worker,db,web:** channel identity linking — channel senders act as their app_user (CH3) ([2aad5f3](https://github.com/open-neko/openneko/commit/2aad5f3e3ebc7477b8bb335b6a8fe634cd08aeda))
* **workflows:** external_event subscription handler + ingress (OL3); verify loop brakes (OL1) ([0fadb1b](https://github.com/open-neko/openneko/commit/0fadb1bb96f09cf431ada62397254b66647cd683))


### Bug Fixes

* **cli:** --runtime inprocess must declare itself to the stack (SEC11 follow-up) ([6d9dfa6](https://github.com/open-neko/openneko/commit/6d9dfa632ab70975b29c3cbd4e8f4e9d92f73215))
* **llm:** GraphJin actor tokens must key off the config secret STRING (GJ4 tail, live-validated) ([9722136](https://github.com/open-neko/openneko/commit/9722136c8d3811643d8b38c648ea70e32636f951))
* **llm:** metric-agent role union covers all offered seats (ST3) ([ac49145](https://github.com/open-neko/openneko/commit/ac4914559022a24ea153ba932d02be27560166fb))
* **llm:** sources-mode probe handles gj_catalog(id:) object shape + wider reload window ([a01db4a](https://github.com/open-neko/openneko/commit/a01db4a5d40fe79a40a2e4bb930a9d4d2b2d80b5))
* **web,llm:** hours-saved is always the last Answer-vitals tile ([77ff585](https://github.com/open-neko/openneko/commit/77ff58500ddf7edfe9beb9bb3383df0b68c4ac11))
* **web,plugin-install:** bundle plugin-install through the web app ([bd14e0e](https://github.com/open-neko/openneko/commit/bd14e0eac1def7af4c382bd87fdf32cc710a6411))
* **worker:** map plugin-suffixed channel plugins and stop hardcoding Telegram thread titles (CH4/CH5 host side) ([94c5622](https://github.com/open-neko/openneko/commit/94c5622a9ecf17689451824a580f6832352e6de1))

## [1.20.0](https://github.com/open-neko/openneko/compare/v1.19.1...v1.20.0) (2026-06-10)


### Features

* **web:** surface hours-saved on the Ask thread + live dashboard sparkline ([#94](https://github.com/open-neko/openneko/issues/94)) ([9092959](https://github.com/open-neko/openneko/commit/909295932d9ff72bb05b4622d92b49732241e8a0))

## [1.19.1](https://github.com/open-neko/openneko/compare/v1.19.0...v1.19.1) (2026-06-05)


### Bug Fixes

* header compile-drop, responsive/UX/styling consistency, and workflow-run reliability ([ed386b9](https://github.com/open-neko/openneko/commit/ed386b949330c0accaa08ef642eb8cfec550d114))
* **web:** finish styling-consistency pass (eyebrows, colors, pills, card borders) ([f55e041](https://github.com/open-neko/openneko/commit/f55e041540ba2f7536e14d7d40e622a4f830abee))
* **web:** hoist [@import](https://github.com/import) block so trailing globals.css rules aren't dropped ([ed89e67](https://github.com/open-neko/openneko/commit/ed89e677353b806aa5ce096cda1fa796c2b02757))
* **web:** resolve cross-page responsive, UX, and styling-consistency issues ([32ec49f](https://github.com/open-neko/openneko/commit/32ec49f11478204260133b462c09b34d76225bcb))
* **workflows:** mark restart-interrupted runs cancelled, retry, sweep zombies ([97eeef4](https://github.com/open-neko/openneko/commit/97eeef44b5400aef858d24778e6390bc40236e30))

## [1.19.0](https://github.com/open-neko/openneko/compare/v1.18.2...v1.19.0) (2026-06-04)


### Features

* **channels:** deliver chat replies back to the origin channel ([57e4840](https://github.com/open-neko/openneko/commit/57e48400db1d0fb9d4e3adbc386ffe834b8b92a9))
* **channels:** isolate channels — web Ask lists only its own threads ([da9e0c5](https://github.com/open-neko/openneko/commit/da9e0c50ee32db0b1065b96f5485348400c56a28))
* **channels:** per-channel rendering + reliable, deduped, dead-lettered delivery ([86e412e](https://github.com/open-neko/openneko/commit/86e412e09d166426c56b8c9bb3e076c08d5c708f))
* **density:** Actions triage queue (list + reading pane) in Compact ([7dae7ec](https://github.com/open-neko/openneko/commit/7dae7ec12c9731e3e5d814ae528df6cafe6bf113))
* **density:** Ask 3-pane with context rail in Compact ([8bb92b6](https://github.com/open-neko/openneko/commit/8bb92b6fb0567a433ddece807d8fd0bce0111334))
* **density:** density toggle + dense dashboard in the real app ([d6dc92b](https://github.com/open-neko/openneko/commit/d6dc92b79e73e7b6a1a0f241f0eaf48051896d72))
* **density:** full Ask context rail — agent-emitted vitals/sources/followups ([63e4d9d](https://github.com/open-neko/openneko/commit/63e4d9dedf9b03995baa5333cb364a7b52fec9d0))
* **density:** mini sparkline on compact briefing tiles ([4cbce45](https://github.com/open-neko/openneko/commit/4cbce4587a5a2b6a0aad5d001a7898001bd70fd8))
* **density:** rebuild header as the mockup's single top bar ([e5d2017](https://github.com/open-neko/openneko/commit/e5d2017275481ae8463483172c7ffdb57ba664f2))
* **density:** Workflows tile grid in Compact ([bfc4019](https://github.com/open-neko/openneko/commit/bfc40191c3d56cfdb80fe18f28dd7a267741d350))
* **hours-saved:** agent-estimated human hours saved, end to end ([cdf4f16](https://github.com/open-neko/openneko/commit/cdf4f16137059b223d8192f80c057c18b2669571))
* **rendering:** Phase 1 — channel-gate a2ui rendering, neutral base prompt ([e624212](https://github.com/open-neko/openneko/commit/e624212bd87d75df41026e1341a3d8ef8c2ae0a2))
* **rendering:** Phase 2 — hermes renders via a real render_cards MCP tool ([d93cd5a](https://github.com/open-neko/openneko/commit/d93cd5ab9f45f9ef4768cfb59cd2f2f73d777246))
* **web:** harden the Ask page chrome — sticky header, solid rails, calmer scroll ([bcfb54a](https://github.com/open-neko/openneko/commit/bcfb54a2cf32fccde0d4a92e724a729070ae5a20))
* **web:** render agent vitals in the work context rail ([fb553cb](https://github.com/open-neko/openneko/commit/fb553cb7f977c159057b3d4e28763d24ee2a84bd))


### Bug Fixes

* **db:** exact memory vector search; drop misconfigured IVFFlat index ([3fc7716](https://github.com/open-neko/openneko/commit/3fc77168fe35a21620ad7294229c159fe0bcd976))
* **density:** approve button uses the purple accent (not dark green) ([549c115](https://github.com/open-neko/openneko/commit/549c1155c177406adef49cd7045259b764dae1cf))
* **density:** expanded-always briefing, no kbd shortcuts, unified approve, header placement ([5afaac9](https://github.com/open-neko/openneko/commit/5afaac983373a3b8043eba5bdc103464a75effa7))
* **llm:** make neko_ask_context fence mandatory for data answers ([2354ab8](https://github.com/open-neko/openneko/commit/2354ab8ebf89fc020ce39ed06734137c0886bd4f))
* **rendering:** thread wantsCards through the OpenShell sandbox path ([a23c8ac](https://github.com/open-neko/openneko/commit/a23c8ac5fd80705a7835b8ad4b010afcf450b3d6))
* **web:** drop FALLBACK_NEXT placeholders from Ask rail ([e1fda34](https://github.com/open-neko/openneko/commit/e1fda34d7e42d6fe569352ca35f2b33277e3e526))
* **web:** pixel-align top bar with dense mockups ([d6d6cf7](https://github.com/open-neko/openneko/commit/d6d6cf7b321f653061a232634f406f6ceb885581))
* **web:** show empty-state in Ask rail instead of a blank column ([b1b5b33](https://github.com/open-neko/openneko/commit/b1b5b335464f02d146562ebc4d6f6405e673510c))

## [1.18.2](https://github.com/open-neko/openneko/compare/v1.18.1...v1.18.2) (2026-06-04)


### Bug Fixes

* **operability:** make failures legible to a non-technical operator ([#86](https://github.com/open-neko/openneko/issues/86)) ([e30a0de](https://github.com/open-neko/openneko/commit/e30a0de84635af3018d8b7a99529d36999fea639))

## [1.18.1](https://github.com/open-neko/openneko/compare/v1.18.0...v1.18.1) (2026-06-04)


### Bug Fixes

* **ci:** resilient embedding prewarm (retry + tolerate build-time network flakiness) ([#84](https://github.com/open-neko/openneko/issues/84)) ([0c0dcce](https://github.com/open-neko/openneko/commit/0c0dcceef26f9f9538675aa9296c9027dff2f288))

## [1.18.0](https://github.com/open-neko/openneko/compare/v1.17.3...v1.18.0) (2026-06-04)


### Features

* OpenShell sandboxed agent runtime + one-command install ([#82](https://github.com/open-neko/openneko/issues/82)) ([42fc357](https://github.com/open-neko/openneko/commit/42fc3570158e5956818f73c5d2e5b3fbd35db475))

## [1.17.3](https://github.com/open-neko/openneko/compare/v1.17.2...v1.17.3) (2026-06-02)


### Bug Fixes

* **worker:** back off and dedupe inbound channel poll failures ([#80](https://github.com/open-neko/openneko/issues/80)) ([15085fb](https://github.com/open-neko/openneko/commit/15085fb00381fb3d536b77ec95a74aadf1054afb))

## [1.17.2](https://github.com/open-neko/openneko/compare/v1.17.1...v1.17.2) (2026-06-02)


### Miscellaneous Chores

* release openneko 1.17.2 ([d738d0c](https://github.com/open-neko/openneko/commit/d738d0c487146f59c86606a44cafa79f28bed679))

## [1.17.1](https://github.com/open-neko/openneko/compare/v1.17.0...v1.17.1) (2026-06-02)


### Bug Fixes

* **release:** copy interaction+channels package.json in Docker build; refresh CLI on deploy ([d946fc0](https://github.com/open-neko/openneko/commit/d946fc0497265d34d527eee48583261e9882a8be))

## [1.17.0](https://github.com/open-neko/neko/compare/v1.16.0...v1.17.0) (2026-05-25)


### Features

* channel CLI install + operator surface (auto-enable inbound + auto-bind) ([#72](https://github.com/open-neko/neko/issues/72)) ([c11230c](https://github.com/open-neko/neko/commit/c11230cc3037fb598a97d303ef44f86b5192f0d2))

## [1.16.0](https://github.com/open-neko/neko/compare/v1.15.0...v1.16.0) (2026-05-25)


### Features

* **openneko:** channel capability in install path + telegram VM install ([#70](https://github.com/open-neko/neko/issues/70)) ([5958691](https://github.com/open-neko/neko/commit/59586916515d73701f3e6a31fc3035496e7da8a9))

## [1.15.0](https://github.com/open-neko/neko/compare/v1.14.1...v1.15.0) (2026-05-25)


### Features

* **channels:** V2 interaction waist + channels + live Telegram channel ([#68](https://github.com/open-neko/neko/issues/68)) ([e71763c](https://github.com/open-neko/neko/commit/e71763cf01ed0eaf19ffb1a0c0a67fc7578afe02))

## [1.14.1](https://github.com/open-neko/neko/compare/v1.14.0...v1.14.1) (2026-05-24)


### Bug Fixes

* **worker:** surface plugin action examples even on marketplace installs ([#66](https://github.com/open-neko/neko/issues/66)) ([a91f125](https://github.com/open-neko/neko/commit/a91f1250c20cc6dc10266fb3b3dc317eede7760b))

## [1.14.0](https://github.com/open-neko/neko/compare/v1.13.9...v1.14.0) (2026-05-24)


### Features

* **workflows:** source-change data triggers + rule surface across backends ([#63](https://github.com/open-neko/neko/issues/63)) ([ca6ed8e](https://github.com/open-neko/neko/commit/ca6ed8ebd20b5fe98e7dc1105231098f5285528a))

## [1.13.9](https://github.com/open-neko/neko/compare/v1.13.8...v1.13.9) (2026-05-23)


### Bug Fixes

* **openneko:** respect externally-set OPENNEKO_VERSION ([bbf0d7a](https://github.com/open-neko/neko/commit/bbf0d7af7f2c0e9ed4b92886a45a37f03f6d7547))

## [1.13.8](https://github.com/open-neko/neko/compare/v1.13.7...v1.13.8) (2026-05-23)


### Bug Fixes

* **ci:** pre-pull pgvector/pgvector:pg16 too for --pull never start ([56ad289](https://github.com/open-neko/neko/commit/56ad28903d077ebd97a5bcf135dab1a86f404f0e))

## [1.13.7](https://github.com/open-neko/neko/compare/v1.13.6...v1.13.7) (2026-05-23)


### Bug Fixes

* **openneko:** add --pull passthrough; smoke uses --pull never after pre-pull ([59b2a46](https://github.com/open-neko/neko/commit/59b2a4647cd5cafd62868783a818e82e1a4c4be2))

## [1.13.6](https://github.com/open-neko/neko/compare/v1.13.5...v1.13.6) (2026-05-23)


### Bug Fixes

* **ci:** retry openneko start in smoke to absorb GHCR manifest race ([d742129](https://github.com/open-neko/neko/commit/d742129b303f63c0f3f4fca2fc745c9c2849d06d))

## [1.13.5](https://github.com/open-neko/neko/compare/v1.13.4...v1.13.5) (2026-05-23)


### Bug Fixes

* **ci:** pre-pull images with retry to defuse post-release-smoke race ([ce366eb](https://github.com/open-neko/neko/commit/ce366ebc901a6b3289aa938ad5c06234dd23595b))

## [1.13.4](https://github.com/open-neko/neko/compare/v1.13.3...v1.13.4) (2026-05-23)


### Bug Fixes

* **work:** drop seq from coalescing-emit after work_run_event column removal ([933c3b6](https://github.com/open-neko/neko/commit/933c3b657ba2c84625c3320256f846a97fb8bc67))

## [1.13.3](https://github.com/open-neko/neko/compare/v1.13.2...v1.13.3) (2026-05-23)


### Bug Fixes

* **config:** propagate rotated DB password to graphjin and neko-migrate ([147cc64](https://github.com/open-neko/neko/commit/147cc64e467f672f8ba9f9efb159aae6bf50f5a2))
* **onboarding:** restore seat-pill styling on CXO toggles ([8ea1e3a](https://github.com/open-neko/neko/commit/8ea1e3ae6ec48d70eefec89a8864028dc906f798))
* **prompt:** rename plugin_actions block to action_tools for tool discovery ([3851649](https://github.com/open-neko/neko/commit/3851649d252bb0afc469921bdc3f2fd00477476e))
* **work:** drop event seq column to eliminate duplicate-key race ([185e5e2](https://github.com/open-neko/neko/commit/185e5e20d6efc1e55de83c789a5193669d61d2bd))

## [1.13.2](https://github.com/open-neko/neko/compare/v1.13.1...v1.13.2) (2026-05-23)


### Bug Fixes

* dedicate neko-migrate one-shot service; break worker/neko-graphjin startup cycle ([#54](https://github.com/open-neko/neko/issues/54)) ([7c1ce78](https://github.com/open-neko/neko/commit/7c1ce7802ba566399dbd6cacc1611b980e925b17))

## [1.13.1](https://github.com/open-neko/neko/compare/v1.13.0...v1.13.1) (2026-05-23)


### Miscellaneous Chores

* cut 1.13.1 to ship the GraphJin 3.18.25 image bump ([a31bc1c](https://github.com/open-neko/neko/commit/a31bc1c447456501436a27d2568e7f14a9bc7ef7))

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
