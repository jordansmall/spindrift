# Changelog

## [0.2.1](https://github.com/jordansmall/spindrift/compare/v0.2.0...v0.2.1) (2026-07-10)


### Features

* **agent:** push-only Box flow for CODE_FORGE=git ([986393a](https://github.com/jordansmall/spindrift/commit/986393a2016620d19175a4efa758ce5e54f01df6))
* **claude-driver:** pin and resume the session across a fix pass ([e456a3c](https://github.com/jordansmall/spindrift/commit/e456a3c9142c926fbead905e729161a880c4c444))
* **drivers:** add lib/drivers/ Driver registry ([181fd93](https://github.com/jordansmall/spindrift/commit/181fd933d65ae177b72797df3af98237c8965308))
* **drivers:** bake the Driver seam into the entrypoint ([bf8e570](https://github.com/jordansmall/spindrift/commit/bf8e57070291bbf69cb926f941813bc79c20a43d))
* **entrypoint:** drive fix-prompt.md when FIX_PASS is set ([4a33a6c](https://github.com/jordansmall/spindrift/commit/4a33a6ca8228eb8168731fb94421fa1622fe59ac))
* **entrypoint:** inject outcome contract into prompt-dir overrides ([29e7268](https://github.com/jordansmall/spindrift/commit/29e7268f88ef4b609672fdb1e461c4332769a830))
* **entrypoint:** render CI_FAILURE_SUMMARY in fix-prompt.md ([ea74a34](https://github.com/jordansmall/spindrift/commit/ea74a34cd61f14e021ada47bd287f8295dcaff86))
* **fix-box:** forward CI failure detail into the fix box ([f1080c3](https://github.com/jordansmall/spindrift/commit/f1080c3243f0a3162bf6e1824860d22b831fa2a6))
* **flakeModule:** expose the driver option ([6688e53](https://github.com/jordansmall/spindrift/commit/6688e53024e783854dcd71ce30cda7d048aafa31))
* **forge:** add Composite to pair an IssueTracker with a CodeForge ([466542b](https://github.com/jordansmall/spindrift/commit/466542bb9b94e53719e5248a2552faa2f75d3443))
* **forge:** add FailureDetail CI capability ([b0db8d6](https://github.com/jordansmall/spindrift/commit/b0db8d60879e7955c4d68bf5a2701abc5916d0f7))
* **forge:** add ListPRFiles to the Client seam ([d3a4738](https://github.com/jordansmall/spindrift/commit/d3a47380385f852dd86f4ca68fb2e1bf8f7a3fc1))
* **forge:** add local IssueTracker adapter ([f601353](https://github.com/jordansmall/spindrift/commit/f601353644e877f498554301879d9dfb3100411a))
* **forge:** add push-only git Code Forge adapter ([d8cac7c](https://github.com/jordansmall/spindrift/commit/d8cac7c3fd1eafed05a9ffa17b4ccfc6ab729034))
* **forge:** classify transient vs genuine push failures ([3e1e4c8](https://github.com/jordansmall/spindrift/commit/3e1e4c8856992bb9de9840184e5c05d3cc4d90d8))
* **forge:** parse a declared ## Touches section ([5a2bfce](https://github.com/jordansmall/spindrift/commit/5a2bfceb57dcaadfedceb5fac74b769e9b75c22c))
* **launcher:** add CODE_FORGE / CODE_FORGE_REMOTE_URL config knobs ([f3542e8](https://github.com/jordansmall/spindrift/commit/f3542e80da3527522e40652e0ab0d5ab75c6125b))
* **launcher:** add doublestar-style glob matcher for merge guard ([2beabfd](https://github.com/jordansmall/spindrift/commit/2beabfd718c92d57ff4f22db2bab3f824ac6c9fc))
* **launcher:** add OVERLAP_GATE config knob ([74cccb0](https://github.com/jordansmall/spindrift/commit/74cccb01d81993f6c9302893cfd60d4e36cc912c))
* **launcher:** detect touch overlap against InProgress issues ([effab6f](https://github.com/jordansmall/spindrift/commit/effab6f388f4efc47f26562ad96fb90d4a8b2783))
* **launcher:** gate dispatchWaves and drainMaxJobs on touch overlap ([803413c](https://github.com/jordansmall/spindrift/commit/803413c263beab0c891b568230c690d7ab1e9498))
* **launcher:** glob-vs-glob touch-set overlap check ([360522b](https://github.com/jordansmall/spindrift/commit/360522b9acbf323b24a37cb913929f9e1b72400a))
* **launcher:** infer touch-sets from open PR files ([485b063](https://github.com/jordansmall/spindrift/commit/485b0633f9a4cb1563db0bdebc8b9d0ab244de24))
* **launcher:** introduce Go Driver strategy seam ([a45666d](https://github.com/jordansmall/spindrift/commit/a45666df22bb3d67dd5ce5ead21dfb64a5e9448a))
* **launcher:** land push-only outcomes without a CI/PR wait ([fa2ad12](https://github.com/jordansmall/spindrift/commit/fa2ad1243d078f4a7791e3d565345b188267e1e7))
* **launcher:** own the ephemeral driver-cache lifecycle ([ddb4a85](https://github.com/jordansmall/spindrift/commit/ddb4a8595302f3980d51d1d6a7678ae9a85c1890))
* **launcher:** retry transient Rebase push failures ([2766dbb](https://github.com/jordansmall/spindrift/commit/2766dbb31196cc9ef8af99081084b4d872ce4d20))
* **launcher:** route run() through wave dispatch on touch overlap ([9d215e2](https://github.com/jordansmall/spindrift/commit/9d215e233f149671e5c9c65494574f4436df876e))
* **launcher:** wire CODE_FORGE to select the CodeForge adapter ([8f9d134](https://github.com/jordansmall/spindrift/commit/8f9d134c12321fa49777619255edadc897bad577))
* **launcher:** wire ISSUE_TRACKER=jira into newIssueTracker ([99a3d15](https://github.com/jordansmall/spindrift/commit/99a3d1536f20ebd8dea93ef930729374aee2b406))
* **launcher:** wire ISSUE_TRACKER=local into the launcher ([cd1761a](https://github.com/jordansmall/spindrift/commit/cd1761a3af84b0bd45b2a091f7a885e3faae3e11))
* **launcher:** wire the merge guard into the post-green gate ([ab91c7f](https://github.com/jordansmall/spindrift/commit/ab91c7f81082020606ce2a80e7d26c243477f7e4))
* **mkHarness:** bake fix-prompt.md into the agent image ([c61f4a0](https://github.com/jordansmall/spindrift/commit/c61f4a07788255e48536c105b3578d6efa2fcd4a))
* **mkHarness:** bake outcome contract at /agent, not /agent/prompts ([b0a3711](https://github.com/jordansmall/spindrift/commit/b0a3711f5ea11d6a4a59864728c05d6b2e401d69))
* **mkHarness:** inject SPINDRIFT_OUTCOME contract into prompts ([3e01433](https://github.com/jordansmall/spindrift/commit/3e01433f6ce6cf182a728ca51b2b78d58aeb772d)), closes [#419](https://github.com/jordansmall/spindrift/issues/419)
* **nix:** add nix run .#regen, the schema-artifact regenerator ([e6ceadb](https://github.com/jordansmall/spindrift/commit/e6ceadb86ff5397154f0209a74ad4c07c307bb1a))
* **nix:** compose an opt-in filer entry into --agents JSON ([70cff73](https://github.com/jordansmall/spindrift/commit/70cff734f7c3e9bc06714f4d9e1bc8aac22abf5b))
* **nix:** extract schema-artifact renderers into lib/renderers.nix ([fedb83b](https://github.com/jordansmall/spindrift/commit/fedb83bed317ed91c0efada8fb8763870f9051e6))
* **prompt:** add comms-discipline section to implementor prompt ([e994156](https://github.com/jordansmall/spindrift/commit/e9941564dbfe6703e5902dff7a47b0815b7b0807))
* **prompt:** gate a FILE ISSUES step on the filer opt-in ([e03ab15](https://github.com/jordansmall/spindrift/commit/e03ab15bc149afd4893a71766f93b912f77f66a8))
* **prompts:** add dedicated fix-prompt template ([14f4f5c](https://github.com/jordansmall/spindrift/commit/14f4f5c0e66ad570d1193271053e8be0c8290885))
* **prompts:** forbid mid-turn narration in conflict-resolve ([20457ea](https://github.com/jordansmall/spindrift/commit/20457ea156e28cb07ae78303e230a04d2af12380))
* **prompts:** forbid mid-turn narration in scout prompt ([7e5e8ab](https://github.com/jordansmall/spindrift/commit/7e5e8ab1bdc03b8185a93eb0837f5da13050f95f))
* **prompts:** forbid narration and cap findings in reviewer ([dd24e76](https://github.com/jordansmall/spindrift/commit/dd24e768282912d1e332fa54a64e5cba04c52f22))
* **prompts:** resolve generated-file conflicts via regeneration ([d3267da](https://github.com/jordansmall/spindrift/commit/d3267daed470c07f0c1ff58e5d0923405485f345))
* **runner:** add writable per-issue driver cache mount ([3be97e6](https://github.com/jordansmall/spindrift/commit/3be97e6e6a7f0aa6f9717b4769f0032458f13bdd))
* **schema:** add ISSUE_TRACKER and Jira config knobs ([b5b58c1](https://github.com/jordansmall/spindrift/commit/b5b58c11a3cd4ee3f3e638b69332c2d52d367f5c))
* **schema:** add MERGE_GUARD_PATHS to the env schema ([e8ca4a4](https://github.com/jordansmall/spindrift/commit/e8ca4a445d920f47fce3f73f17095edc3574cbc2))
* **schema:** register CODE_FORGE and CODE_FORGE_REMOTE_URL knobs ([de44d87](https://github.com/jordansmall/spindrift/commit/de44d87c3829a2b12ff3586b79d0b1e260147cc3))
* **schema:** register OVERLAP_GATE in the env schema ([a638ae4](https://github.com/jordansmall/spindrift/commit/a638ae47bcab3cbb9eb2007da91fed7451f89f4c))


### Bug Fixes

* **agent:** gate filer-prompt.md read on filer opt-in ([729e3ee](https://github.com/jordansmall/spindrift/commit/729e3eed613d36b40ff6f6bf669e7cfb19e813d0))
* **box:** provision git identity repo-locally, not globally ([c6f7471](https://github.com/jordansmall/spindrift/commit/c6f747156d703400f4981a8f010c76e87341da14)), closes [#404](https://github.com/jordansmall/spindrift/issues/404)
* **checks:** exempt DRIVER from env-schema coverage ([e263c32](https://github.com/jordansmall/spindrift/commit/e263c32a6f2c8e9a9a27e80850002967baa034d5))
* **entrypoint:** inject prompts only for agents present ([e32fdf6](https://github.com/jordansmall/spindrift/commit/e32fdf6d79052b246b64befc6f994e68439fc73c))
* **forge:** add PredecessorLabel; restore SwapLabel behavior ([8c4d139](https://github.com/jordansmall/spindrift/commit/8c4d1393dd5836c2d0d8670333b47de715b36d46))
* **forge:** capture git stderr on rebase push failure ([147386c](https://github.com/jordansmall/spindrift/commit/147386c95249d1e904d439755d045abd6cfd2c96)), closes [#367](https://github.com/jordansmall/spindrift/issues/367)
* **forge:** checkout main before reading its identity in test ([0d8313c](https://github.com/jordansmall/spindrift/commit/0d8313c9ef3275d15cd90f082cf108dc70cf6c97))
* **forge:** clean up the from-label after a mapped Jira transition ([351e945](https://github.com/jordansmall/spindrift/commit/351e945907f42af8404ea2b75e7b43a9a4e76d2d))
* **forge:** exclude done-category issues from Jira ListIssues ([14d9dc5](https://github.com/jordansmall/spindrift/commit/14d9dc5cc1f60717759c7c809046db3ea5a8401f))
* **forge:** fix Probe to use positional slug and surface stderr ([a795bfc](https://github.com/jordansmall/spindrift/commit/a795bfcd7fdaa026da9b7d8b0b65c33ef5a462a1))
* **forge:** local Issue.Labels includes the dispatch-state marker ([c11eb09](https://github.com/jordansmall/spindrift/commit/c11eb09e0c986af112a710643c51c0c0ad7af880))
* **forge:** make TransitionState explicit with from+to states ([ae1827f](https://github.com/jordansmall/spindrift/commit/ae1827fe55487cd1fb69b7c1ec1baab59c41e0cc))
* **forge:** map Jira statusCategory to the OPEN|CLOSED contract ([3d71e57](https://github.com/jordansmall/spindrift/commit/3d71e57224986e959069576226a625f59e488bfb))
* **forge:** pin test's other clone to origin/main ([d32cdde](https://github.com/jordansmall/spindrift/commit/d32cdde3991b10457af7ab6d7cef1e3e291924b5))
* **forge:** reject flag-like refs in the git Code Forge (RCE) ([52ef285](https://github.com/jordansmall/spindrift/commit/52ef2855d018b4737485003a102e842dcf143eb7))
* **forge:** set commit identity on the git Code Forge's temp clone ([0d554c5](https://github.com/jordansmall/spindrift/commit/0d554c5f33f2ec229524c259143ebb6813ec1005))
* **launcher:** bound touch-glob overlap check to polynomial time ([e5a8e64](https://github.com/jordansmall/spindrift/commit/e5a8e646536cb0be6be1c63b3fc21cf50e49dee9))
* **launcher:** don't PR-state-check a git-forge merged outcome ([6f167d4](https://github.com/jordansmall/spindrift/commit/6f167d4e3c4040c454a7961ecbe7cb5854511def))
* **launcher:** reject MERGE_MODE=auto fast for CODE_FORGE=git ([4836ccb](https://github.com/jordansmall/spindrift/commit/4836ccb49ec599e15c7cf134db3a01a5c74c37d9))
* **launcher:** report each doctor seam's own probed slug ([ac7a326](https://github.com/jordansmall/spindrift/commit/ac7a326fae650e0605f839e04d454a2fb01a56d0))
* **launcher:** route retry classification through Driver ([9ff1e7e](https://github.com/jordansmall/spindrift/commit/9ff1e7eea4fea8160aeb550e112aa75096624aef))
* **launcher:** skip re-rebase after conflict-resolve ([8b177ce](https://github.com/jordansmall/spindrift/commit/8b177ce99c795ba8ea7071c082dcf176db2b81f4))
* **nix:** add git to launcher-go-test sandbox PATH ([dc163b7](https://github.com/jordansmall/spindrift/commit/dc163b7ee256347ecc727e9c2b740bf234b06ef4))
* **nix:** allowlist FILER_MODEL as box-env-only ([3619159](https://github.com/jordansmall/spindrift/commit/36191593c6f4e97739f7383205ec912ea9d32b09))
* **nix:** disable cgo in launcher-go-vet/-test checks ([9b1d74b](https://github.com/jordansmall/spindrift/commit/9b1d74b996c5edd21272c30c03c430292582131b))
* **nix:** guard regen against running outside the spindrift repo ([503c257](https://github.com/jordansmall/spindrift/commit/503c2571d74e343715f22f774c18e5b3db48f0e8))
* **prompts:** reviewer diffs against fetched origin base ([9d3843c](https://github.com/jordansmall/spindrift/commit/9d3843c1f7751ff40045de424962eb16603c5949)), closes [#405](https://github.com/jordansmall/spindrift/issues/405)
* **runner:** scope the cache mount to .claude/projects ([c914e74](https://github.com/jordansmall/spindrift/commit/c914e740863d6811b33055e634ff54860a303298))
* **schema:** regenerate harness.env.example after quote fix ([be83315](https://github.com/jordansmall/spindrift/commit/be83315e3aed735f7ed0e3809f7d0eb02211ace6))
* **schema:** remove stray issueTracker from repository example ([bb68e62](https://github.com/jordansmall/spindrift/commit/bb68e62567c88dc380082dc355ad9f8045f6265d))
* **template:** add overlapGate to settings example ([679bc86](https://github.com/jordansmall/spindrift/commit/679bc86c3ded5da85cf487d8eb8d2b054d77cf87))
* **tests:** give session-resume tests a fresh WORK_DIR per run ([5633a9d](https://github.com/jordansmall/spindrift/commit/5633a9d54c6db7d126ed0012e314e1be1f4e32d6))
* **tests:** quote conflict-resolve loop, drop order dependence ([7089bce](https://github.com/jordansmall/spindrift/commit/7089bce8be0e98d88e2b07715a4e3b60f8625fab))
* **usage:** keep filer role in the token-usage breakdown ([21583e1](https://github.com/jordansmall/spindrift/commit/21583e120d874e236b6bccd43a3d156875f57a40))


### Documentation

* add Filer term to the glossary ([e6b1696](https://github.com/jordansmall/spindrift/commit/e6b1696e8e74b5ac401405023a9307d100fec369))
* cover runtime prompt-dir overrides in outcome-contract notes ([b2f06d7](https://github.com/jordansmall/spindrift/commit/b2f06d74a83e42c499ff8af96ec8c3f5e590191b))
* describe the declared ## Touches overlap gate ([ee4a1a4](https://github.com/jordansmall/spindrift/commit/ee4a1a4be34884fe8d554cdff7ab5e7a6d199ab1))
* describe the inferred touch-set overlap augmentation ([dab282a](https://github.com/jordansmall/spindrift/commit/dab282a390ffc83587af88dc445a4dc36f14e2f4))
* document FILER_MODEL, filerPrompt, and the Filer ([350a896](https://github.com/jordansmall/spindrift/commit/350a89619415e2bd45e0cfcd1d46f6b947296b9d))
* document nix run .#regen in CONTRIBUTING.md ([88d8815](https://github.com/jordansmall/spindrift/commit/88d8815ceeba08a7e809e55e2a5df5a24610f2dd))
* document the driver option ([0b78c65](https://github.com/jordansmall/spindrift/commit/0b78c65181a892cf0f2e47a101c05effa6fc0d8b))
* document the harness-owned outcome contract ([4c1f4d0](https://github.com/jordansmall/spindrift/commit/4c1f4d06ae7f480c8c253ff9653c061a4ca17935))
* document the jira Issue Tracker config surface ([195a807](https://github.com/jordansmall/spindrift/commit/195a8078dadf1eebc192fc7b48455694f80e9d5e))
* document the merge guard (MERGE_GUARD_PATHS) ([d7ad4fb](https://github.com/jordansmall/spindrift/commit/d7ad4fb6a990d8afff097e12965a67b08137a8e6))
* **forge:** document the local issue tracker ([40d6054](https://github.com/jordansmall/spindrift/commit/40d605422288689b7d8fb54d77150f94e6d8f48d))
* **harness:** describe SCOUT_MODEL/REVIEW_MODEL per-agent omission ([9ab1403](https://github.com/jordansmall/spindrift/commit/9ab14038ece95d5faf2d61fef8687a3f268068ab))
* **harness:** fix stale pair-omission wording in reference.md ([b5f35da](https://github.com/jordansmall/spindrift/commit/b5f35da4bf3a00c9c38a971fc554dff87c323ee2))
* **outcome:** document pr= as a landing reference, not always a URL ([8bcfedf](https://github.com/jordansmall/spindrift/commit/8bcfedf91433425b68b955da8b83435943e542ec))
* point README's CONTRIBUTING.md row at nix run .#regen ([5603183](https://github.com/jordansmall/spindrift/commit/56031834274b315f1f0ea6e76d6dafd9c940ec55))
* reconcile jira docs with the concurrently-landed local tracker ([e10b047](https://github.com/jordansmall/spindrift/commit/e10b047e7fc4fa59c22cb5c43e0d3243de7fd30b))
* **reference:** document CI_FAILURE_SUMMARY ([14eb901](https://github.com/jordansmall/spindrift/commit/14eb90152c2617bd56e232250d268caee92b8aa5))
* **reference:** document the ephemeral session cache ([fe62c29](https://github.com/jordansmall/spindrift/commit/fe62c293100397fbe0e7a60fbfb47171e4809e2e))
* **reference:** document the hermetic git config guarantee ([ccdb33c](https://github.com/jordansmall/spindrift/commit/ccdb33c02a6b6de112fdc28f0d3b8159064ba556))
* **reference:** note the fix box's fix-prompt.md ([bb94ec2](https://github.com/jordansmall/spindrift/commit/bb94ec208d9013eefa733c188afe5ce37c06d463))
* **runner:** fix stale .claude mount path in comments ([a2b7e00](https://github.com/jordansmall/spindrift/commit/a2b7e009bf7168ebf83646b85f5ddf2c65faa195))


### Code Refactoring

* **forge:** consolidate fake-gh helper in Probe tests ([85f9711](https://github.com/jordansmall/spindrift/commit/85f9711975853f15bbf83359453e1ceb2e9c2ba4))
* **forge:** split Client into IssueTracker + CodeForge seams ([a1027a8](https://github.com/jordansmall/spindrift/commit/a1027a835a2160db78358c7ae5f3650a7d5f1d9e)), closes [#328](https://github.com/jordansmall/spindrift/issues/328)
* **harness:** bake --agents JSON per-agent ([a386c0b](https://github.com/jordansmall/spindrift/commit/a386c0b554373bedb668eaeaf58830cf420cce85))
* **nix:** dedupe template-settings-example's groupToAttr ([284a027](https://github.com/jordansmall/spindrift/commit/284a0275d5fc172e3c6736ebb276d9d46a5113f2))
* **nix:** wire drift-guard checks to lib/renderers.nix ([0bb150d](https://github.com/jordansmall/spindrift/commit/0bb150d58b05d6a42f219ec16f44203f8a39433e))


### Tests

* **box:** assert entrypoint identity is repo-local, not global ([64a1baa](https://github.com/jordansmall/spindrift/commit/64a1baa4d28797421aca8abddadb1e802bcd69b4))
* **checks:** guard outcome-contract injection and drift ([7096c2b](https://github.com/jordansmall/spindrift/commit/7096c2b14cd5f016852b3a285d354e0fffaac1be))
* **entrypoint:** cover FIX_PASS prompt-selection branch ([12031d9](https://github.com/jordansmall/spindrift/commit/12031d962160d8783e3b387461b6eddec6f405ad))
* **entrypoint:** cover missing contract file and marker parity ([1724563](https://github.com/jordansmall/spindrift/commit/172456374ebb079cda63c00a78a835b128becba5))
* **entrypoint:** demo generated-file conflict resolution ([07acf35](https://github.com/jordansmall/spindrift/commit/07acf35c94650212e64615eb14cf6aebd29f2f49))
* **harness:** cover scout-only and reviewer-only --agents shapes ([ed3adac](https://github.com/jordansmall/spindrift/commit/ed3adacc9a55973d0982a80f19691c4fb00cf8eb))
* **launcher:** cover dispatch-level PR-file overlap gate ([287e0ed](https://github.com/jordansmall/spindrift/commit/287e0ed19507047425fdd4ab7186a39e47247e41))
* **launcher:** cover merge-guard error path and auto mode ([5b52f23](https://github.com/jordansmall/spindrift/commit/5b52f23e48ed2d7ecda524ce36837927151cbf61))
* **launcher:** cover the git-forge merge-blocked path ([2582371](https://github.com/jordansmall/spindrift/commit/25823711a7c769b77e8582d3d7d0b35a482dc4e5))
* **prompt:** assert COMMS section enforces machine-log voice ([4ce2341](https://github.com/jordansmall/spindrift/commit/4ce23411b718ac2ebf3eae5b9961925be7c70482))
* **prompt:** scope COMMS assertions to the COMMS section ([402229a](https://github.com/jordansmall/spindrift/commit/402229a94c9850d0a6d273935c4abcb4c188d39e))


### Continuous Integration

* add least-privilege permissions block to workflow ([f787b80](https://github.com/jordansmall/spindrift/commit/f787b80f90c07e69a86f9492eee01a72a1310c4d))
* cache the nix store across runs ([9faa93f](https://github.com/jordansmall/spindrift/commit/9faa93f0070bccd77034e63fc5dceafd62f876a4)), closes [#432](https://github.com/jordansmall/spindrift/issues/432)


### Styles

* **checks:** nixfmt the outcome-contract checks ([79199f2](https://github.com/jordansmall/spindrift/commit/79199f2d71785b17e2838550db7b23c9219291f3))
* **mkHarness:** nixfmt lib/mkHarness.nix and nix/checks.nix ([8a55709](https://github.com/jordansmall/spindrift/commit/8a5570980d02358be64e7b588c3fc2af17c07419))

## [0.2.0](https://github.com/jordansmall/spindrift/compare/v0.1.3...v0.2.0) (2026-07-09)


### ⚠ BREAKING CHANGES

* **cli:** `spindrift engage <issue>` is removed. Use `spindrift recover <issue>` instead. This removal targets v0.2.0 as previously announced.

### Features

* bump implementor model default to claude-sonnet-5 ([67f7038](https://github.com/jordansmall/spindrift/commit/67f70388c3269f178f2ae7572bcf2dcedfd1c2f3))
* **checks:** add nix-fmt nixfmt formatting gate ([7913576](https://github.com/jordansmall/spindrift/commit/7913576c64bbfa9bd2bbd310522183e24027972a)), closes [#361](https://github.com/jordansmall/spindrift/issues/361)
* **cli:** retire deprecated engage alias ([495ec78](https://github.com/jordansmall/spindrift/commit/495ec78824ef8246f53c527708d34ef635961645))
* **dispatch:** add agent-recover label workflow ([d6d769b](https://github.com/jordansmall/spindrift/commit/d6d769bed2dab9602bf56cbd15bf170dbe4aa486))
* **dogfood:** bake nil into the agent toolchain ([682374c](https://github.com/jordansmall/spindrift/commit/682374cfa855180407e52fcd545bf471c61db504))
* **flakeModule:** replace defaults with settings ([3715625](https://github.com/jordansmall/spindrift/commit/37156253cf658646c7fdfe789a53cbc88be695cd))
* generate flake-options reference, drift-guard template ([ab4d932](https://github.com/jordansmall/spindrift/commit/ab4d932a835e3442b267f527a914cd5d76ba4f16))
* **nix:** expose formatter output via flakeModule ([278e4d6](https://github.com/jordansmall/spindrift/commit/278e4d6f89abad1a474192947c8af9c49c5fc5e8))
* **review:** make reviewer subagent aggressively adversarial ([c52ee05](https://github.com/jordansmall/spindrift/commit/c52ee053736a455dcd919a7896972378b0b30934))
* **schema:** promote 13 knobs to consumer-tunable flakeOption ([fa6f2d4](https://github.com/jordansmall/spindrift/commit/fa6f2d4e35cbc9a8b1890c13ced13c561d8a2e98))


### Bug Fixes

* **nix:** chown nix/var to agent uid in unprivileged box ([6aeeea1](https://github.com/jordansmall/spindrift/commit/6aeeea18a2cb604e24bd3f10c358223b093c446f)), closes [#356](https://github.com/jordansmall/spindrift/issues/356)
* **nix:** reformat mkHarness.nix with nixfmt ([d86c3af](https://github.com/jordansmall/spindrift/commit/d86c3af81ef1644e02724f50f49ac881d1b2198b))
* **nix:** suppress SIGPIPE in nix-var-owned-by-agent check ([b105f21](https://github.com/jordansmall/spindrift/commit/b105f21f3f6764d23b23a711195cfd7b02b4d584))
* **nix:** update stale comment in flakeModule.nix ([f634ce4](https://github.com/jordansmall/spindrift/commit/f634ce42cc68b52212b41f5de24a797bf4b5c032))
* reformat .nix files with pinned nixfmt ([3ad5136](https://github.com/jordansmall/spindrift/commit/3ad5136860cc8ad037f5055e09705823f090490a))
* **test:** correct REPO_SLUG empty-default grep in nix check ([a9f3b02](https://github.com/jordansmall/spindrift/commit/a9f3b020a2225d586ae578e12f4b0acc21a82a1f))


### Documentation

* add flake-options reference and discovery path guide ([09426f3](https://github.com/jordansmall/spindrift/commit/09426f3e762e75b522656c9d9252ed175f1ae9df))
* add selfHealing and repository sections to ([a1a4f90](https://github.com/jordansmall/spindrift/commit/a1a4f90bba5d0919b7c19eb1a2419253aad8b22a))
* **adr:** record grouped-settings surface ([22a6440](https://github.com/jordansmall/spindrift/commit/22a64403e2bf21c89866a9aa29dd99ee1fb2b6ac))
* instruct agent to run nil diagnostics on nix changes ([3084393](https://github.com/jordansmall/spindrift/commit/30843936bcea519ddc99213da0a8d187cc3b28ca))
* record prompt-integrity vocabulary and ADR 0016 ([9ec5f58](https://github.com/jordansmall/spindrift/commit/9ec5f5874486c32582ba09c8d4035595ce962a5c))
* **template:** add selfHealing and repository sections ([d447bef](https://github.com/jordansmall/spindrift/commit/d447bef7ce3cb89d4defa82131aada5f0a918992))
* update for settings.&lt;section&gt; surface ([eb615c9](https://github.com/jordansmall/spindrift/commit/eb615c9d1116f874e4485a1da036ea5cfe831a11))
* update reference for widened flake settings surface ([ef1d41c](https://github.com/jordansmall/spindrift/commit/ef1d41c6b60e1631d6007c3023a0e51db8078a78))


### Code Refactoring

* dogfood and template use settings.* ([e395991](https://github.com/jordansmall/spindrift/commit/e3959917739ce3c4a17f3bd4988d692784652668))
* **nix:** correct formatter comment accuracy ([14cbf8c](https://github.com/jordansmall/spindrift/commit/14cbf8c76e39bfb3c7d4d90be9b34cfd5a36a451))


### Tests

* assert MODEL schemaFlags default is claude-sonnet-5 ([b2e7c35](https://github.com/jordansmall/spindrift/commit/b2e7c35f36c61d533528fff94e367df75ca800a4))
* **bats:** update MODEL default assertions to claude-sonnet-5 ([879b23a](https://github.com/jordansmall/spindrift/commit/879b23a064f8ef39ac9bd3a04c653a2c16721c61))
* **go:** add REPO_SLUG precedence and required-validation tests ([a1c37c0](https://github.com/jordansmall/spindrift/commit/a1c37c011469a40b2a982dd9cbca49f252804d2f))
* **nix:** add formatter-is-nixfmt checks ([4b92945](https://github.com/jordansmall/spindrift/commit/4b9294536415eee9ed4d22dd09ee83e4b9360df2))
* **nix:** also assert unknown knob within valid section throws ([6d99941](https://github.com/jordansmall/spindrift/commit/6d9994110ce877e067acce711bbc7522bcf106d2))
* **nix:** assert nil is present in the dogfood toolchain ([d72cd17](https://github.com/jordansmall/spindrift/commit/d72cd172f2adbdbabb39fe0185607723e72be93f))
* **nix:** assert nix/var owned by agent uid ([41af773](https://github.com/jordansmall/spindrift/commit/41af773e6ac59cad37b95e7fd32e8b341db3ca44))
* **nix:** assert promoted operator knobs bake via settings ([dbdb512](https://github.com/jordansmall/spindrift/commit/dbdb512348b3a034839d1a65ebc34ab9ca0b061a))

## [0.1.3](https://github.com/jordansmall/spindrift/compare/v0.1.2...v0.1.3) (2026-07-08)


### Features

* **cli:** add man page and split help into concise + full reference ([e832152](https://github.com/jordansmall/spindrift/commit/e8321525607e62abcfb665be3e164d9d832bf605))
* **doctor:** offer to create missing triage labels ([9f7fa0e](https://github.com/jordansmall/spindrift/commit/9f7fa0ec25098057942d664eb83d9d7408861e62))
* **doctor:** report triage label status ([3e180fc](https://github.com/jordansmall/spindrift/commit/3e180fcef49dde657a30f6b4c548d296dca13170)), closes [#316](https://github.com/jordansmall/spindrift/issues/316)
* **entrypoint:** log cold-run toolchain nudge ([20bf5ee](https://github.com/jordansmall/spindrift/commit/20bf5eea53bb85a766d8fe4f0e176e513a6b00aa))
* **entrypoint:** run post-clone lifecycle inside devShell ([2f84cdd](https://github.com/jordansmall/spindrift/commit/2f84cddad3ff0dabcdcab3bcae046f4fe8b52391))
* **forge:** add CreateLabel() to Client interface ([0a6383e](https://github.com/jordansmall/spindrift/commit/0a6383e8b150d405cebc0548175b590b4d2d2214))
* **forge:** add ListLabels() to Client interface ([45f9dc5](https://github.com/jordansmall/spindrift/commit/45f9dc5964b3dd8e3efd9830eaeec05442726ed4))
* **forge:** add Probe() connectivity check to Client ([3e54167](https://github.com/jordansmall/spindrift/commit/3e54167ef0fba58c014373e2a2f27f717d13ea40))
* **launcher:** add doctor subcommand ([725c274](https://github.com/jordansmall/spindrift/commit/725c274774fa91e94c1c0a859fa7546fc76edfb6))
* **prompt:** rebase before push, handle push failures ([d6766ef](https://github.com/jordansmall/spindrift/commit/d6766eff59436ed3d51aaebf288efc24cd25e541))
* **runner:** reap container on success, retain on failure ([f62a561](https://github.com/jordansmall/spindrift/commit/f62a5611b775d16e96d1b88d56bbe9fa0eb58b52))
* **schema:** add DEV_SHELL_NAME knob ([e54f429](https://github.com/jordansmall/spindrift/commit/e54f4294b9822ab45012aae997175d921e7be6bc))


### Bug Fixes

* **doctor:** guard against nil Stat() result in TTY detection ([d9ca2c7](https://github.com/jordansmall/spindrift/commit/d9ca2c7788eb36d9098b9c8d2ad9bffc3afafe2b))
* **entrypoint:** suppress SC2016 on prefetch wrapper printf ([1c953d0](https://github.com/jordansmall/spindrift/commit/1c953d093af60cfe5c215933d083312f2d353c3c))
* export MODEL into devShell wrapper; test coverage ([6510cce](https://github.com/jordansmall/spindrift/commit/6510cce01c6bc9f10df67e14f657f626d770d051))
* reviewer findings — agents word-split, README clarity ([d710fba](https://github.com/jordansmall/spindrift/commit/d710fba39eac26383d3e0b00ddd08e0f22d5ef6b))


### Documentation

* add Before you deploy section and tighten prerequisites ([40936f0](https://github.com/jordansmall/spindrift/commit/40936f0b4f3f9e19ef8357c3f9e9870af50f2f45))
* add CI and release badges to README ([655053e](https://github.com/jordansmall/spindrift/commit/655053ed5fc69819a7213d64a79a06e8962a3033))
* add CONTRIBUTING and SECURITY reporting guides ([4130ae3](https://github.com/jordansmall/spindrift/commit/4130ae3333ba56ceeeec8f2379977f0dac5360b0))
* add documentation index ([39df1b0](https://github.com/jordansmall/spindrift/commit/39df1b0e774ee2e673b31c0dd01b27879a7c3568))
* compress basic-flow diagram ([86962a1](https://github.com/jordansmall/spindrift/commit/86962a1f9e2ae684e45a46859b4de1ccec14e912))
* create docs/reference.md and move CLI section ([7c4e7e7](https://github.com/jordansmall/spindrift/commit/7c4e7e7082db8ee825677f2ff73fe08ea2732626))
* document cold-run toolchain nudge ([b53a51f](https://github.com/jordansmall/spindrift/commit/b53a51fa1a4803a33e8713feb83eb9b8d6e3f7b7))
* document devShell-first toolchain sourcing (ADR 0014) ([f2ec41c](https://github.com/jordansmall/spindrift/commit/f2ec41cac71fd98c2360a534958a9f4b63e3f0ff))
* document doctor subcommand in help and README ([3b36f2e](https://github.com/jordansmall/spindrift/commit/3b36f2e37d553c6f23b7ae67affb10cfe944901b))
* drop three-roles section, link CONTEXT.md from pitch ([0289776](https://github.com/jordansmall/spindrift/commit/02897767fb0db003341c7418694907a29e5c7d9e))
* move flake config variants to reference ([d64d69a](https://github.com/jordansmall/spindrift/commit/d64d69af045706793ee7b4157395a7ffe0945702))
* move runtime config and advanced tuning to reference ([5274cf4](https://github.com/jordansmall/spindrift/commit/5274cf47506037aa237fa1fd2d8faa4b98c924b5))
* move runtime flow and label lifecycle to reference ([d3ae173](https://github.com/jordansmall/spindrift/commit/d3ae1732969e01a26e1ea9e243bb1b3a2942d0de))
* move security, macOS, and remaining sections to reference ([a0f3f7d](https://github.com/jordansmall/spindrift/commit/a0f3f7d9be4dc45e50c5c135365e5c2103937fae))
* **readme:** document DEV_SHELL_NAME and lean CI shell ([ce70ed5](https://github.com/jordansmall/spindrift/commit/ce70ed5a01f3c9c719d00eb86e1b3b2831698edb))
* require a dedicated worktree per task ([e2e3fa7](https://github.com/jordansmall/spindrift/commit/e2e3fa71432b9722063f615fd81456c4c328756c))
* retire fanout-blocker from glossary and ADRs ([a7ec43e](https://github.com/jordansmall/spindrift/commit/a7ec43e23448f77de341ade684e2e61f581f6f30))
* split Forge into Issue Tracker and Code Forge seams ([ab3ee3d](https://github.com/jordansmall/spindrift/commit/ab3ee3d4d115a72e4a00e4c9b27997b67f99558f))
* update devShell lifecycle-wrapping description ([362e7be](https://github.com/jordansmall/spindrift/commit/362e7beb054441024371b134ffd84e94505037eb))
* update doctor description for label-status reporting ([de1f152](https://github.com/jordansmall/spindrift/commit/de1f152c1ea4d26a847a34e03052217459c55701))
* use American spelling of "realize" ([c2a8f2f](https://github.com/jordansmall/spindrift/commit/c2a8f2f826a039bc3dc122e6d533bfe32a308b7e))


### Code Refactoring

* **launcher:** remove fanout-blocker barrier ([dff3ab5](https://github.com/jordansmall/spindrift/commit/dff3ab540a071a27b443cd45bda12272030851ff))


### Tests

* assert DEV_SHELL_NAME=default targets .#default ([0098e45](https://github.com/jordansmall/spindrift/commit/0098e451313a463e32ca76b6eeafa4afeb2f59e8))
* remove barrier and stall-guard bats tests ([60ebc64](https://github.com/jordansmall/spindrift/commit/60ebc64500eac6c15aebe01b6fa0e59e1b066a0f))
* **runner:** cover reap decision and no-rm invariant ([62318eb](https://github.com/jordansmall/spindrift/commit/62318ebe6107e39e9eaa8be85a4dba3c64955c75))


### Continuous Integration

* use spindrift CLI in agent-dispatch, not deprecated nix aliases ([0eafbe4](https://github.com/jordansmall/spindrift/commit/0eafbe4878f88c17b429606b85e6e631344ffcfd)), closes [#334](https://github.com/jordansmall/spindrift/issues/334)


### Miscellaneous Chores

* **dogfood:** remove barrier export and stall guard ([5b79a68](https://github.com/jordansmall/spindrift/commit/5b79a68b5d05383883df9be122c64f31fefe5caa))
* drop BARRIER_LABEL from schema and config ([95273f2](https://github.com/jordansmall/spindrift/commit/95273f2d80ac5b66153fad8e455d94069a78071f))
* **gitignore:** ignore .envrc ([2b51072](https://github.com/jordansmall/spindrift/commit/2b51072a5fa6fd8bc29c07d7efa0bc643d39ee05))

## [0.1.2](https://github.com/jordansmall/spindrift/compare/v0.1.1...v0.1.2) (2026-07-07)


### Features

* **cli:** add --version, dispatch verb, updated help ([4fbf0fe](https://github.com/jordansmall/spindrift/commit/4fbf0fece704f77d143f048cabea72e97fae7779))
* **cli:** add recover verb; deprecate engage as warn-then-exec alias ([9fe9321](https://github.com/jordansmall/spindrift/commit/9fe9321440be3804a86af15128dd69ce984e0111))
* **cli:** dispatch --no-build flag ([a735b14](https://github.com/jordansmall/spindrift/commit/a735b14360137c60d342a3ea309c95cdd293e1b9))
* **cli:** register preview verb and add to help text ([6f4c74a](https://github.com/jordansmall/spindrift/commit/6f4c74acc86437bebe88c6fb90b38cf1e135aec5))
* **flags:** variadic dispatch args and --yes/--force ([ab9a59f](https://github.com/jordansmall/spindrift/commit/ab9a59f2cbce0a274df502722aa6c7d0d4873ac9))
* **forge:** add CanAutoMerge and EnqueueAutoMerge to Client ([8cf20d6](https://github.com/jordansmall/spindrift/commit/8cf20d6391ed17aed9dbddc14453c7439f9ca32d))
* **forge:** add PRForBranch; fix fake OpenPRForBranch state filter ([70a0502](https://github.com/jordansmall/spindrift/commit/70a05026277cb799d740379a84b37523e3d886f5))
* **heartbeat:** add review, plan, search, git phases ([9ebce36](https://github.com/jordansmall/spindrift/commit/9ebce369b190b583306aaa86fe3eecc3d3098626)), closes [#306](https://github.com/jordansmall/spindrift/issues/306)
* **heartbeat:** attribute activity via role switch headers ([f12c9a1](https://github.com/jordansmall/spindrift/commit/f12c9a1d2b8655f19590eed956738973c936722b))
* **heartbeat:** show agent model in switch header ([db6d5da](https://github.com/jordansmall/spindrift/commit/db6d5da6c98d95b5a9a4d7c69a1bf0bf24109698)), closes [#307](https://github.com/jordansmall/spindrift/issues/307)
* **launcher:** add MERGE_MODE knob; decouple agent-complete from merge ([58c9e7a](https://github.com/jordansmall/spindrift/commit/58c9e7ad989d72c6a299e3a7ba3b82ef54360821))
* **launcher:** add preview verb core logic ([58ee04c](https://github.com/jordansmall/spindrift/commit/58ee04ce96d267533a62e7a48c51e5b25fefdcba))
* **launcher:** implement MERGE_MODE=auto with preflight check ([a65de17](https://github.com/jordansmall/spindrift/commit/a65de17e9a60a7072e92896bc761fb81e5b54fb5))
* **launcher:** selective list dispatch and preview annotations ([42b342a](https://github.com/jordansmall/spindrift/commit/42b342a9b82da0c931cd3f2c5de4aa8f0a8e6cb5))
* **nix:** deprecate apps.build with migration notice ([9f974e4](https://github.com/jordansmall/spindrift/commit/9f974e4583dba639560648f36b1e0f040659c3be))
* **nix:** spindrift CLI package, apps.default, ldflags, run deprecation ([8a1c0c2](https://github.com/jordansmall/spindrift/commit/8a1c0c2ca3b402e37fa8ae7f8e9d477bdc8cdc47))
* **preview:** annotate blockers in bare queue listing ([7b6b6b0](https://github.com/jordansmall/spindrift/commit/7b6b6b044d4c02ce503c053fb98c47955db9f0a2))
* **runner:** add IsReady() to Runner interface ([efad797](https://github.com/jordansmall/spindrift/commit/efad7974216c8bc24f9e69a0b02eb3d4ab84db87))


### Bug Fixes

* **bats:** use SPINDRIFT_CMD for engage tests ([a2da8f0](https://github.com/jordansmall/spindrift/commit/a2da8f008c553ab73147e38f852071defd52dff7))
* **heartbeat:** guard emit() to skip bare FormatHeartbeat lines ([fe51736](https://github.com/jordansmall/spindrift/commit/fe51736c4c38ade89bedce5abe4258cbb9df5c51))
* **launcher:** add dispatch startup banner; update harness.env.example ([c632a3e](https://github.com/jordansmall/spindrift/commit/c632a3e87e784b5b274bba55e2db245a23244728))
* **launcher:** bind blockerReady to PR-merged/closed reality ([35906e5](https://github.com/jordansmall/spindrift/commit/35906e549d0c601fe4c65cf1447d4638895964b7))
* **launcher:** don't demote to agent-failed on post-green merge block ([6094a38](https://github.com/jordansmall/spindrift/commit/6094a3836bb373359122a46ca87b233a15fdf5b5))
* **launcher:** gate verifyMerged to immediate mode only ([fb8f5d9](https://github.com/jordansmall/spindrift/commit/fb8f5d9b5b2a7ca91ef6d37161e46ed430b83606))
* **launcher:** sync flagtable_gen.go for auto MERGE_MODE ([70f26ba](https://github.com/jordansmall/spindrift/commit/70f26bac46a8875cdf1679b1cedd7e0f0c060cb6))
* **launcher:** use PRForBranch in blockerReady for merged-PR check ([a7dac52](https://github.com/jordansmall/spindrift/commit/a7dac523be3db80f74991674a7b345091807da1c))
* **nix:** self in top-level module arg, thread revision to fixtures ([bb99066](https://github.com/jordansmall/spindrift/commit/bb9906607ce23f5ea9acc8403d0470591375600e))
* **template:** sync harness.env.example for auto MERGE_MODE ([d93810a](https://github.com/jordansmall/spindrift/commit/d93810a97cdf08644d56d2842471497cfdc19418))


### Reverts

* remove agent-dispatch.yml change (workflow scope missing) ([7b4987c](https://github.com/jordansmall/spindrift/commit/7b4987c9fa583b956ca58456de6563a166d1d1d9))


### Documentation

* add MIGRATING.md, .envrc template, devShell-first quick-start ([24900a7](https://github.com/jordansmall/spindrift/commit/24900a7cfa371039d9b1b23c9c49a234b5ab8a3c))
* **launcher:** update mergeMode comment for auto ([4337a18](https://github.com/jordansmall/spindrift/commit/4337a18d004359b9e14bd25fe419cdf5eab7a395))
* **migrating:** document engage → recover rename ([20b21d1](https://github.com/jordansmall/spindrift/commit/20b21d1ab6d3c962b50b384a1ef444a0c48266c5))
* **readme:** document spindrift CLI and MERGE_MODE contracts ([4efd88c](https://github.com/jordansmall/spindrift/commit/4efd88c300c2131ca514c1604d8a05709b9fe4ad))
* **schema:** update MERGE_MODE=auto description ([ebea4e7](https://github.com/jordansmall/spindrift/commit/ebea4e74c7f0a79768f8730ec4de48f5a15f8e27))


### Code Refactoring

* **heartbeat:** remove throttle param from New() call sites ([d4446b2](https://github.com/jordansmall/spindrift/commit/d4446b2b97bd0f44253a20f3672dccc99cc86c6f))
* **launcher:** rename engageByNumber/Issue to recoverByNumber/Issue ([2f7cf8d](https://github.com/jordansmall/spindrift/commit/2f7cf8d0db5e5a5ac4d1456fb26edacf3f2f8272))
* **launcher:** stable eviction notice order ([1af7975](https://github.com/jordansmall/spindrift/commit/1af79759af082e40a4d4e9b377c44a55da1a2865))
* rename image output spindrift -&gt; agent-image ([9781a82](https://github.com/jordansmall/spindrift/commit/9781a8287f136b1bcd42dc4d7a82f9c75a7719c4))


### Tests

* **bats:** add DEPS_WAIT_SECS guardrail to setup() ([9ad4cea](https://github.com/jordansmall/spindrift/commit/9ad4cea849521d74ec7da38f982b8778e1abacf9))
* **bats:** label issue in --no-build positional test ([c1a0c48](https://github.com/jordansmall/spindrift/commit/c1a0c488a3e16b04377d0d9f88ef80d6c0275661))
* **bats:** pin MERGE_MODE=immediate in merge-dependent tests ([928a2e3](https://github.com/jordansmall/spindrift/commit/928a2e30d25ff62c98f141dc44a59f5082b894e7))
* **bats:** pre-seed [#1](https://github.com/jordansmall/spindrift/issues/1) as ready to fix wave-ordering CI hang ([cb19bc2](https://github.com/jordansmall/spindrift/commit/cb19bc2ef47b47bfbbe20ef402545a875ed182c3))
* **bats:** update blocker tests to PR-state predicate ([14fe9d8](https://github.com/jordansmall/spindrift/commit/14fe9d8e5abaea13483debab139bd2a4af711e3d))
* **bats:** wire FAKE_GH_PR_LIST_1 in wave-ordering test ([c87c98d](https://github.com/jordansmall/spindrift/commit/c87c98d92f5b985e0687eea64da539e188e38d51))
* **launcher:** cover auto MERGE_MODE enqueue, fallback, preflight ([4304281](https://github.com/jordansmall/spindrift/commit/43042810e645d73f0fc22cc9d9016f462dc1c8a4))
* **launcher:** cover blockerReady merged/closed/open-with-label cases ([d34d754](https://github.com/jordansmall/spindrift/commit/d34d75434c39adda281f4ee027ed3acb59692cef))
* **launcher:** cover preview verb ([a587002](https://github.com/jordansmall/spindrift/commit/a587002b4889fc219eb80e1ada2c4d795974dbaf))


### Continuous Integration

* build agent-image attr instead of spindrift ([47601da](https://github.com/jordansmall/spindrift/commit/47601daa835e149d4c474c4f3c5a5a3936b882e6))


### Miscellaneous Chores

* **nix:** bake MERGE_MODE=immediate into dogfood harness ([618d006](https://github.com/jordansmall/spindrift/commit/618d0060037f5929cd6904f421ead3d3148735cf))
* wire MERGE_MODE into schema, flag table, CI, and docs ([bbfd02c](https://github.com/jordansmall/spindrift/commit/bbfd02c4b14fa84144123b9c0b1506cce4fa20a7))

## [0.1.1](https://github.com/jordansmall/spindrift/compare/v0.1.0...v0.1.1) (2026-07-07)


### Features

* add build and run launcher scripts ([e7f1c35](https://github.com/jordansmall/spindrift/commit/e7f1c3599eee7a97fee4ddd1626ffca09dbd2c73))
* add in-container agent entrypoint and issue prompt ([68bbdf7](https://github.com/jordansmall/spindrift/commit/68bbdf78561df3e073bd9311a3f73769be1a92d5))
* add lib.mkHarness engine ([86fda1e](https://github.com/jordansmall/spindrift/commit/86fda1ec535c10f3b732a0da6659e88d5c181e7a))
* add nix-built build and run commands ([e7bd37c](https://github.com/jordansmall/spindrift/commit/e7bd37cd74763bb6b7460ba19cd946f2069fece7))
* add subagent model tiers and complete label ([70a1433](https://github.com/jordansmall/spindrift/commit/70a143304eece865f4de7766f729743aa48c0968))
* add templates.default consumer starter ([793aa0e](https://github.com/jordansmall/spindrift/commit/793aa0eb94aaedd0f7091ef4775f4609f1646ab8))
* **box:** add stream-json transcript formatter ([eb990d8](https://github.com/jordansmall/spindrift/commit/eb990d8889731fbebfb30b6a7e6cfb1395d31873))
* **box:** bake selected skills into the image ([219260e](https://github.com/jordansmall/spindrift/commit/219260e065e5d0cb0caf6f4949ed8905b305d9be))
* **box:** mount skills dir for headless agent ([99b4d4d](https://github.com/jordansmall/spindrift/commit/99b4d4d13cf44a5a3e1fd44cef71aa0650ac7b9f)), closes [#118](https://github.com/jordansmall/spindrift/issues/118)
* **box:** stream the agent transcript for live observability ([a7bd4be](https://github.com/jordansmall/spindrift/commit/a7bd4bea2b788450c474fea6af630c96019660c9)), closes [#113](https://github.com/jordansmall/spindrift/issues/113)
* **build:** realize the image, fall back to a container ([884037f](https://github.com/jordansmall/spindrift/commit/884037fae50aa1a29dddcf5df0e5e98b2449498d))
* **classify:** recognize session-limit as retryable rate-limit ([adb4e63](https://github.com/jordansmall/spindrift/commit/adb4e6340986e92ef05bb9a7b14ff62a95740cba))
* dogfood mkHarness and wire flake checks ([628653f](https://github.com/jordansmall/spindrift/commit/628653fc3bd1978565addfc8f29e97acee50a1c0))
* **dogfood:** enable fan-out behind fanout-blocker barrier ([3af21ff](https://github.com/jordansmall/spindrift/commit/3af21ff22821427bb6e60ec469107a2d105ba342)), closes [#175](https://github.com/jordansmall/spindrift/issues/175)
* **engine:** thread lifecycle label defaults ([34230b3](https://github.com/jordansmall/spindrift/commit/34230b345b811b84c48c4a940a0908d1047157d5))
* **entrypoint,prompt:** auto-detect flake devShell; guide agent to nix develop ([2ab6457](https://github.com/jordansmall/spindrift/commit/2ab64571a5b6b2becc165895565fd293ac2a1e95))
* **entrypoint:** pipe transcript through formatter ([829439a](https://github.com/jordansmall/spindrift/commit/829439a8976c95de9dbe912168af12096d2d9fda))
* **entrypoint:** resolve pre-work rebase conflicts with an agent ([28568cf](https://github.com/jordansmall/spindrift/commit/28568cfa959589f474dfa5c743a0002cf3e2debd))
* **env:** register ISSUE_NUMBER dispatch knob ([bcdea43](https://github.com/jordansmall/spindrift/commit/bcdea4359b2043f4d498afb804d4d9c8ba3c784e))
* **flake:** add flake-parts shim over mkHarness ([7fa5264](https://github.com/jordansmall/spindrift/commit/7fa5264b16d97aba59c42a026905bcdb7a39df04))
* **flake:** declare meta.license = MIT on package outputs ([0f1960e](https://github.com/jordansmall/spindrift/commit/0f1960e29d0f557d248a2805ba9b4b3f75862068))
* **flake:** dogfood packages/apps through the shim ([dcd1fd9](https://github.com/jordansmall/spindrift/commit/dcd1fd98792965e8af2c0f21bc7967f28dd4013e))
* **forge:** add Comment() seam for posting issue comments ([4afc641](https://github.com/jordansmall/spindrift/commit/4afc641f13d1222b93403ba98eb67fa3949b33b1))
* **forge:** agent-driven conflict resolution at merge gate ([bc93d46](https://github.com/jordansmall/spindrift/commit/bc93d46afe091e0cc93e085ef43eef59e3082dd7))
* **forge:** ErrMergeConflict sentinel and Rebase method ([159a686](https://github.com/jordansmall/spindrift/commit/159a68660821cce674ecb21f3ee9ed117729d718))
* **harness:** promote nixInBox to true default; add lean escape hatch ([4d69c3a](https://github.com/jordansmall/spindrift/commit/4d69c3acb47c5bcec95cd4aa6bca7e7fc4699fc5))
* **heartbeat:** add tool-to-phase mapping ([9271cee](https://github.com/jordansmall/spindrift/commit/9271cee2e2f1eabd20478d9260aea8a17ca166dc))
* **heartbeat:** collapse tool calls into count line ([dfd36ef](https://github.com/jordansmall/spindrift/commit/dfd36ef76e755278524773be6d916c7a5579de4f))
* **heartbeat:** in-box cleaned heartbeat view ([#183](https://github.com/jordansmall/spindrift/issues/183)) ([926c8b0](https://github.com/jordansmall/spindrift/commit/926c8b08ba0884c79c978aba878e501f15a89a77))
* **heartbeat:** include phase tag in heartbeat line format ([871aa66](https://github.com/jordansmall/spindrift/commit/871aa66175aa7dba6f51f4a301120f04e2f967f9))
* **heartbeat:** live coarse milestones from agent stream ([83fe3bd](https://github.com/jordansmall/spindrift/commit/83fe3bdf09da51f22cf362fb08a2c87b1238012d)), closes [#182](https://github.com/jordansmall/spindrift/issues/182)
* **heartbeat:** surface assistant narration as heartbeat lines ([a5f92b0](https://github.com/jordansmall/spindrift/commit/a5f92b009962c253a649807ebe1f181a94283aac))
* **heartbeat:** tag narration lines with current phase ([fa501a1](https://github.com/jordansmall/spindrift/commit/fa501a1810c75c4aa6b57ae8d66abbc98484ddb1))
* **heartbeat:** track phase in Writer, emit on transition ([9f08a4d](https://github.com/jordansmall/spindrift/commit/9f08a4db54981a927a87b5476470e2358ad19ac4))
* **image:** apply content-hash tag on load; gate run on it ([b89d71c](https://github.com/jordansmall/spindrift/commit/b89d71c5238a0e5b01bdd8f12a49b2a4d0e90667))
* **image:** bake content-hash IMAGE_TAG into OCI launcher preamble ([72da237](https://github.com/jordansmall/spindrift/commit/72da2374f468e9e75cf93476fea3a46a708bc6ba))
* **image:** run the Box as a non-root user ([b9b3986](https://github.com/jordansmall/spindrift/commit/b9b3986d3b05db8ec364d69a926c6811707bb6f7)), closes [#64](https://github.com/jordansmall/spindrift/issues/64)
* **launcher:** --&lt;name&gt;-file flags for secret knobs ([bae5cf9](https://github.com/jordansmall/spindrift/commit/bae5cf984ae7b319621061988ba4379fd39d865e))
* **launcher/forge:** Client interface + adapters ([52b52f7](https://github.com/jordansmall/spindrift/commit/52b52f719e4526f26488a1479ced2c07053f176e))
* **launcher:** add alias field to flagEntry, parseFlags, printHelp ([c946fcf](https://github.com/jordansmall/spindrift/commit/c946fcfde4754c10cb7fa913b1f0f85136407f7d))
* **launcher:** add doc/dflt fields to flagEntry ([e3aa50f](https://github.com/jordansmall/spindrift/commit/e3aa50fbaf6ad8e25588d6911f2760b2247b6f33))
* **launcher:** adopt discovered PR when outcome line absent ([6417feb](https://github.com/jordansmall/spindrift/commit/6417febee501370cdfd826b0e049f576884463c9))
* **launcher:** dispatch a single ISSUE_NUMBER directly ([b152057](https://github.com/jordansmall/spindrift/commit/b152057aa1b2fc73981f38d4f65669a8aebe15ad))
* **launcher:** exit 2 on empty queue, exit 3 on drained queue ([c269e72](https://github.com/jordansmall/spindrift/commit/c269e72afc0d1983a297a2fc7c21d02065e8c2a2))
* **launcher:** format durations as h/m/s in usage comment ([bea6f54](https://github.com/jordansmall/spindrift/commit/bea6f549ba3717a598ac732ba2f6168032e04e38))
* **launcher:** hold-until-reset + bounded retry ([8529d7c](https://github.com/jordansmall/spindrift/commit/8529d7c93eed3e5d156c56d640c8c4d66dd4bba1))
* **launcher:** intercept --help/-h before flag parsing ([32bb614](https://github.com/jordansmall/spindrift/commit/32bb61464621990fc98b59d48fb585628bb25071))
* **launcher:** internal/outcome package ([e827891](https://github.com/jordansmall/spindrift/commit/e8278912c2d41e064c492617cfb83713ffdd28c9))
* **launcher:** internal/runner package — Runner seam (OCI, bwrap, Fake) ([f1ad575](https://github.com/jordansmall/spindrift/commit/f1ad575fad79a59bb126827afa932ad9c6d293e5))
* **launcher:** post usage-stats comment on issue completion ([34673b9](https://github.com/jordansmall/spindrift/commit/34673b917ed694a9e30421f9777db692ba940a3c)), closes [#200](https://github.com/jordansmall/spindrift/issues/200)
* **launcher:** schema-derived CLI flags (flag &gt; env &gt; default) ([b5be8f2](https://github.com/jordansmall/spindrift/commit/b5be8f2915023e40600effcdd2270207889cc367))
* **launcher:** self-heal red pipeline with capped fix-agent ([febc8ca](https://github.com/jordansmall/spindrift/commit/febc8ca48a284cde1a2943fbc081d13892d1be5f))
* **launcher:** skip redundant claim when pre-claimed ([9d0310b](https://github.com/jordansmall/spindrift/commit/9d0310b0dd14b26bce38dab85cec7a231e6c326a))
* **launcher:** surface exit class in outcome report ([8a608a0](https://github.com/jordansmall/spindrift/commit/8a608a0c4a9b1f41c6b1d757a5c0786c36a36990))
* make harness core language-agnostic with prefetch hook ([4804769](https://github.com/jordansmall/spindrift/commit/48047691f3d6b6142d4ea08bdba6f20cfc7e6c20))
* **mkHarness:** add configurable run defaults and container runtime ([d80b675](https://github.com/jordansmall/spindrift/commit/d80b675a3e42167eae692d84dc69a57740502efd))
* **model:** tier agent models per role ([09440e3](https://github.com/jordansmall/spindrift/commit/09440e3b0ff935da8d4e74170ba43989bb5e1583)), closes [#156](https://github.com/jordansmall/spindrift/issues/156)
* **nix:** extend flag-table generator with alias field ([abbc708](https://github.com/jordansmall/spindrift/commit/abbc7081ff2dd40ff20ac049b2a3b4a71e1d7309))
* **nix:** extend flag-table generator with doc/dflt/secrets ([963cafd](https://github.com/jordansmall/spindrift/commit/963cafdc011ec05f9a04d76b80e050c0ad3ba83d))
* **nix:** generate --agents JSON with builtins.toJSON ([39983c3](https://github.com/jordansmall/spindrift/commit/39983c3c8bc060fdf911cd689cf9cdac78e1a5d1)), closes [#62](https://github.com/jordansmall/spindrift/issues/62)
* **nix:** generate and verify flag table from env-schema ([0639331](https://github.com/jordansmall/spindrift/commit/063933143185b89079cafd27a3c6ff00639ced2b))
* **outcome:** classify transient vs terminal exits ([7ba1e04](https://github.com/jordansmall/spindrift/commit/7ba1e045b53d34b78c96b057f7e090171e42f111))
* promote agent model to the run defaults ([bfcffc0](https://github.com/jordansmall/spindrift/commit/bfcffc02b9f71eb3b910705f25ecce9308e6cb76))
* **prompt:** add scout + reviewer subagent pipeline ([d505dda](https://github.com/jordansmall/spindrift/commit/d505ddae87186828709c609d4cf321b96449818f))
* **prompt:** green-watch, rebase-merge, outcome ([c7d8368](https://github.com/jordansmall/spindrift/commit/c7d83685115d3074eb40fd5697dda3b64567ca29))
* **prompt:** honor SPINDRIFT_PROMPT_DIR for zero-rebuild override ([2c614eb](https://github.com/jordansmall/spindrift/commit/2c614eb0b0f00458e6cab0e24a663d808face578))
* **prompt:** per-agent prompt files, object --agents JSON ([aaad2e2](https://github.com/jordansmall/spindrift/commit/aaad2e293d520b775990f8fb8447f0d76b01708f))
* **prompt:** prefer skill when present, fall back to inline ([1b3c6c7](https://github.com/jordansmall/spindrift/commit/1b3c6c74108734d440b9ff94e84dc64de9835379))
* **prompt:** push branch after every commit ([f5e21ef](https://github.com/jordansmall/spindrift/commit/f5e21eff36118971de6b3edb2bdd425d4c036b79))
* **prompt:** render configurable prompt to a mounted store path ([2f19274](https://github.com/jordansmall/spindrift/commit/2f1927440b15ba2668261361a226cc61fbfba058))
* **prompt:** require reviewer subagent, drop inline-review fallback ([d6f67b8](https://github.com/jordansmall/spindrift/commit/d6f67b88b82f277402ea258a947ffd946eb46afa)), closes [#49](https://github.com/jordansmall/spindrift/issues/49)
* **prompt:** verify MERGED + complete-label before emitting the outcome line ([0d866aa](https://github.com/jordansmall/spindrift/commit/0d866aa101b4c6945dda8ec3b556df3cf9383862))
* **run,dogfood:** cap batch with MAX_JOBS; add serial rebuild loop ([ba68e23](https://github.com/jordansmall/spindrift/commit/ba68e23f73754235faab99f72b9b0177e3860669))
* **run:** add BARRIER_LABEL knob for dispatch fencing ([6c8c7ad](https://github.com/jordansmall/spindrift/commit/6c8c7adde431a029a1e5356da17c4f5c10b7306f)), closes [#174](https://github.com/jordansmall/spindrift/issues/174)
* **run:** add end-of-run outcome report ([96a264e](https://github.com/jordansmall/spindrift/commit/96a264eab3519da6986fda6f092df3e78f128d3f))
* **run:** add Go launcher binary ([d781295](https://github.com/jordansmall/spindrift/commit/d78129516952465c264536690dae772e18b43aaa))
* **run:** add Go launcher binary ([c972bdc](https://github.com/jordansmall/spindrift/commit/c972bdc908493cddb01a506b62935f6c6e0323d5))
* **run:** add merge_when_green CI gate ([b25dd86](https://github.com/jordansmall/spindrift/commit/b25dd868bb9708c544d944bd55c82b4948852e05))
* **run:** build Box image on demand when it's missing ([044d6c4](https://github.com/jordansmall/spindrift/commit/044d6c426d95bf28e4822e9c14cc3491d15677dd)), closes [#56](https://github.com/jordansmall/spindrift/issues/56)
* **run:** daemonless bubblewrap runner — run Box from nix store without OCI ([86ac97f](https://github.com/jordansmall/spindrift/commit/86ac97fdc63d0884f16e3cbc1ce84e22517f52d6))
* **run:** dependency-wave ordering in launcher ([aeb754e](https://github.com/jordansmall/spindrift/commit/aeb754e2a3f43630d93032990dd91684c6c001b0)), closes [#39](https://github.com/jordansmall/spindrift/issues/39)
* **run:** engageByNumber and engage subcommand ([d969d3d](https://github.com/jordansmall/spindrift/commit/d969d3d72e9a16cc7a0818ea289e848cc6f16ae7)), closes [#195](https://github.com/jordansmall/spindrift/issues/195)
* **run:** make dispatch idempotent via label swaps ([b1a0096](https://github.com/jordansmall/spindrift/commit/b1a00960d7d2a46047b5ac6ad29ce1b113890158))
* **run:** port dep-wave orchestration into Go launcher ([5b49b9e](https://github.com/jordansmall/spindrift/commit/5b49b9e0d6123949cffc018fbceb56b938579536))
* **run:** verify each box's real PR and label state in outcome report ([636380b](https://github.com/jordansmall/spindrift/commit/636380b96e779b41d59db7b828ddb45e4b81f4fe))
* **run:** wire buildGoModule launcher into mkHarness run wrapper ([83ed880](https://github.com/jordansmall/spindrift/commit/83ed880576f0c262df5e7f8e0ebb4ba54eef7a35))
* **run:** wire buildGoModule launcher into mkHarness run wrapper ([5e62612](https://github.com/jordansmall/spindrift/commit/5e62612416b6a47786ee82883845ed4a772425e8))
* **schema:** add BARRIER_LABEL consumer-tunable knob ([30eb943](https://github.com/jordansmall/spindrift/commit/30eb943e6b3f4ac75773d5e7d8ad03afd1638328))
* **schema:** add MAX_REBASE_ATTEMPTS knob ([667adee](https://github.com/jordansmall/spindrift/commit/667adeed69f0be63ca7d8fe95e3fe0316ccbe98a))
* **usage:** add BreakdownByRole for per-subagent attribution ([94d32e0](https://github.com/jordansmall/spindrift/commit/94d32e0ad6dd44122860db6777a147df3e3dc78a))
* **usage:** add FormatDuration for human-readable h/m/s output ([9318908](https://github.com/jordansmall/spindrift/commit/931890848a05c6ae722d5583bfe369724f93a013))
* **usage:** add internal/usage parser for result events ([ea63299](https://github.com/jordansmall/spindrift/commit/ea632995d11241945aa86c11c5a80c5502e933c7))


### Bug Fixes

* **box:** add error handler to force-with-lease push ([3a9644c](https://github.com/jordansmall/spindrift/commit/3a9644c7315e76b95eb6f439c74f95ad402bec1f))
* **box:** bound devShell probe with configurable timeout ([5b4c76c](https://github.com/jordansmall/spindrift/commit/5b4c76ca0e0723de58aa46a2e7d91ce9498b22a7)), closes [#99](https://github.com/jordansmall/spindrift/issues/99)
* **box:** force-reset stale branch on re-dispatch ([559492d](https://github.com/jordansmall/spindrift/commit/559492de61726360a6c9711f5d4d0ba45be770a9))
* **box:** force-with-lease + surfaced gh errors ([bb7984b](https://github.com/jordansmall/spindrift/commit/bb7984bd535f03f918c9ee1636a779ac8cf91f66))
* **box:** make DEV_SHELL_PROBE_TIMEOUT a flakeOption so it is nix-baked ([879ae6f](https://github.com/jordansmall/spindrift/commit/879ae6fea44cbc4102e6195bd1104b93353592f2))
* **box:** preserve raw stream-json in issue log ([feceb8a](https://github.com/jordansmall/spindrift/commit/feceb8a4ac1b1a1fec08351ddb2a1a97bad89aab))
* **box:** publish rebased branch after adoption-path rebase ([d19a3d1](https://github.com/jordansmall/spindrift/commit/d19a3d1d3f7abbdb428f6e9084607ca1a79e92d9))
* **box:** rebase agent branch onto latest main before work ([65837cc](https://github.com/jordansmall/spindrift/commit/65837ccd0b5c6c55277724bfad8e5b061991ab45))
* **box:** skip force-reset when open PR exists ([dad868c](https://github.com/jordansmall/spindrift/commit/dad868cd8c45ab3185fd73aba7416895df332c5e))
* **box:** treat gh failure as hard abort in force-reset ([89515fb](https://github.com/jordansmall/spindrift/commit/89515fb4deede3e80abb2960955f6e8d35e18296))
* **box:** use derivation .name for skill filename ([e7e0b45](https://github.com/jordansmall/spindrift/commit/e7e0b4569b24f5329b9c40d2da8b19a9cda79e3c))
* **build:** surface genuine nix errors; stage artifacts outside consumer tree ([c482673](https://github.com/jordansmall/spindrift/commit/c4826735d1b3b318b6eec403c8d43e9ec6f81fe8))
* **build:** warn on mutable nixBuilderImage; drop skopeo note ([cf6735b](https://github.com/jordansmall/spindrift/commit/cf6735bcdd6618d1ac6554976b1d24bd571cb779))
* **check:** assert packages-baked without sandbox store access ([942d2bd](https://github.com/jordansmall/spindrift/commit/942d2bd6870a9867bb5bbb09ec41476782f64b5c))
* **check:** make the bats suite pass under the Linux sandbox ([a2c4bd5](https://github.com/jordansmall/spindrift/commit/a2c4bd56b3ca6dd21b13bca0ac91f4c9d813f7fd))
* **checks:** match ./-prefixed nix.conf path in nix-conf-in-image ([e7269f9](https://github.com/jordansmall/spindrift/commit/e7269f97915c21a3e5191401ee210efc84bd3ca7))
* **checks:** read the image once in nix-conf-in-image ([29f0b80](https://github.com/jordansmall/spindrift/commit/29f0b8075e1883f49b5d4ca4fdfa845efd7290ce))
* **dispatch:** release the claim when an issue is blocked ([ec47a11](https://github.com/jordansmall/spindrift/commit/ec47a114f43a4e6fcef0594812bd3100a452d1e6))
* **docs:** keep harness.env.example generated; security guidance lives in README/CLAUDE ([aa67202](https://github.com/jordansmall/spindrift/commit/aa6720204b4190e5c90a307e20755af7c17dab23))
* **dogfood:** checkout base branch before pull and rebuild ([468cda3](https://github.com/jordansmall/spindrift/commit/468cda305fc801435ce79187bbf9b3821535e1d7))
* **dogfood:** guard against no-progress iterations ([7a63bd1](https://github.com/jordansmall/spindrift/commit/7a63bd1b6129ba036834327ff0830dd95692327b)), closes [#132](https://github.com/jordansmall/spindrift/issues/132)
* **entrypoint:** avoid shellcheck directive false-positive ([94c4b24](https://github.com/jordansmall/spindrift/commit/94c4b24b4a2416be482720b1832b514c5a48c0ad))
* **fixtures:** add ghFakeOverlay to skillsHarness ([8742d61](https://github.com/jordansmall/spindrift/commit/8742d618ca19de26f5d95d6abd19ed9916018db9))
* **forge:** filter CLOSED issues out of Fake.ListIssues ([3d47042](https://github.com/jordansmall/spindrift/commit/3d470425f5ddfaa411f83bdadb83caeacda4aa1c))
* **forge:** sort fake ListIssues oldest-first ([9950b6d](https://github.com/jordansmall/spindrift/commit/9950b6d49efd2838a87ff40d28ae613c6bdc843b))
* **harness:** add nix/var/nix/db to mkdir; add nix-conf-in-image check ([869745e](https://github.com/jordansmall/spindrift/commit/869745e405ed4250269da2c86dc5827b08b268fd))
* **harness:** bake conflict-resolve-prompt into image and promptDir ([079eb7c](https://github.com/jordansmall/spindrift/commit/079eb7c7c6658a29d6c7e4267929cfcef1395ef4))
* **launcher/forge:** address review findings ([1538cb5](https://github.com/jordansmall/spindrift/commit/1538cb54dd7ee9922cb57624e486280d3e1ada5c))
* **launcher/outcome:** address review findings ([1e64268](https://github.com/jordansmall/spindrift/commit/1e6426875a120bd24a5d911ffee0e23e8076c2cb))
* **launcher:** gate claiming by maxParallel semaphore in fanOut ([bae0676](https://github.com/jordansmall/spindrift/commit/bae0676ec7296e4954775b99f8b6bbc63abd7cd9))
* **launcher:** gate on statusCheckRollup.state ([cc16870](https://github.com/jordansmall/spindrift/commit/cc168704120d8197162fe4c05662aab22365f4c0))
* **launcher:** harden queryOpenPRByBranch error handling ([95fa63f](https://github.com/jordansmall/spindrift/commit/95fa63f143884c1ceeef2477f1cd72dca11e7799))
* **launcher:** surface CheckState errors in mergeWhenGreen ([e9a5962](https://github.com/jordansmall/spindrift/commit/e9a59625a2eb907a961037e154e9f871dd0534e9))
* **mkharness:** bake RUNTIME into bwrap run launcher only, not build ([e870b50](https://github.com/jordansmall/spindrift/commit/e870b50dde8e7120ec8fd95c6e52d464fb8c9b4b))
* **nix:** bake OCI build vars into goRunPreamble so run's EnsureReady can build on demand ([1e5491b](https://github.com/jordansmall/spindrift/commit/1e5491babde775964bc1b2dfc2fa6fc743fbc5b9))
* **nix:** clarify header comment in flagtable_gen.go ([868e699](https://github.com/jordansmall/spindrift/commit/868e699e1daae683f113e789f8c791bca0d40815))
* **nix:** hard-error on unknown defaults key in mkHarness ([fcaeb89](https://github.com/jordansmall/spindrift/commit/fcaeb8917b7d6113e3aa432e847ab6e3c7710a67)), closes [#97](https://github.com/jordansmall/spindrift/issues/97)
* **outcome:** address reviewer blockers ([0cb421f](https://github.com/jordansmall/spindrift/commit/0cb421f47daded1ce31e792067988d49b374b84a))
* **prompt:** clarify PR_URL substitution in rollup wait ([d2a67c6](https://github.com/jordansmall/spindrift/commit/d2a67c678fa0c353a1cf06d8710367e82f0d4b48))
* **prompt:** forbid backgrounding the CI wait; require outcome line last ([eb1695c](https://github.com/jordansmall/spindrift/commit/eb1695c6b7ea74dcac39585942509da26e52b8c3))
* **prompt:** harden TDD rule, make commit guidance self-contained ([f24b0d0](https://github.com/jordansmall/spindrift/commit/f24b0d06ad30ca790a08ff16c0f9c445c4d36f16))
* **prompt:** replace gh pr checks with GraphQL rollup wait ([0d7b893](https://github.com/jordansmall/spindrift/commit/0d7b8933e93e0662947893bc67c97380385a518b))
* **prompt:** use --force-with-lease for in-box pushes ([7af5283](https://github.com/jordansmall/spindrift/commit/7af528381e660dc8f213c10800f2494b110a8983)), closes [#217](https://github.com/jordansmall/spindrift/issues/217)
* **prompt:** wait for CI to register before trusting the green-watch ([31f3a43](https://github.com/jordansmall/spindrift/commit/31f3a430270b6c36bec23c1d7edf4ad5b2fbd015)), closes [#70](https://github.com/jordansmall/spindrift/issues/70)
* **run:** add --unshare-user so --uid/--gid take effect in bwrap sandbox ([08e5627](https://github.com/jordansmall/spindrift/commit/08e562797ed1b7f838bf188982513675d0216d23))
* **run:** bake prompt into image and dispatch oldest-first ([0e192a8](https://github.com/jordansmall/spindrift/commit/0e192a85a9a8efe15f857bab4cc79b2e03b51519)), closes [#63](https://github.com/jordansmall/spindrift/issues/63)
* **run:** bound inline blocker parse to contiguous ref list ([beb9a0f](https://github.com/jordansmall/spindrift/commit/beb9a0fd1b9810784a8358642b2335b11f7b2417))
* **run:** clamp MAX_PARALLEL to &gt; 0 to avoid unbuffered semaphore deadlock ([9494fc1](https://github.com/jordansmall/spindrift/commit/9494fc131ca3ee5237bb1b0b6f8381506d044c84))
* **run:** confirm green stability before merging ([af7b32d](https://github.com/jordansmall/spindrift/commit/af7b32d7f20e5a24f6f6bf299d455ce2098e7c16))
* **run:** detect ## Blocked by edges and gate MAX_JOBS on readiness ([0ab8359](https://github.com/jordansmall/spindrift/commit/0ab83595a7e8a2a2f1bec8f61bdf6dd240bf8484))
* **run:** emit status=malformed for unparseable outcome lines ([af2a99b](https://github.com/jordansmall/spindrift/commit/af2a99b75213c9bc6603497a3af1d1990015fbb7))
* **run:** fail dependents fast when in-batch blocker fails ([a2a7c9e](https://github.com/jordansmall/spindrift/commit/a2a7c9e86e3b10e5e42ddad656ef24bf14f2d96d))
* **run:** gate each issue's PR immediately after its box exits ([7b3458c](https://github.com/jordansmall/spindrift/commit/7b3458cd4ad541689c7687a4cde182cbea79fde1)), closes [#125](https://github.com/jordansmall/spindrift/issues/125)
* **run:** gate on positive check success ([234b971](https://github.com/jordansmall/spindrift/commit/234b971ca39cad915733eca718a79d3f5b4e1a28))
* **run:** increase outcomeLine scanner buffer to 4 MiB ([e7d2951](https://github.com/jordansmall/spindrift/commit/e7d29515b79399dbebca0b4e073ab4a2f5f8bf26))
* **run:** oldest-first query + cap warning ([c0ccc60](https://github.com/jordansmall/spindrift/commit/c0ccc60d563fe0bba005ef06970ae56fdecd588a))
* **run:** pause before confirmation poll ([6e75aa0](https://github.com/jordansmall/spindrift/commit/6e75aa01eeae9f35ae465ee9768e9821c644e331))
* **run:** reap stale same-named container before dispatch ([aa0e5f7](https://github.com/jordansmall/spindrift/commit/aa0e5f7a81346878819e0161b7bfeee9e3f0fa40)), closes [#73](https://github.com/jordansmall/spindrift/issues/73)
* **run:** rebase-retry on merge conflict in merge gate ([587044f](https://github.com/jordansmall/spindrift/commit/587044fcc74f302f0c5c8b1bc69c84b99cebbd5c))
* **run:** reconcile stranded agent-in-progress issues on start ([862b20e](https://github.com/jordansmall/spindrift/commit/862b20efa39fe27e8291ed73e1fbd402eca69f34)), closes [#193](https://github.com/jordansmall/spindrift/issues/193)
* **run:** skip barrier filter on ISSUE_NUMBER targeted dispatch ([efd4925](https://github.com/jordansmall/spindrift/commit/efd4925804c49ba3f7973fc044407a9e446106f8))
* **run:** skip stale-reap for running containers ([d3144de](https://github.com/jordansmall/spindrift/commit/d3144de96a2e6925ba1ffab470571e736926b207))
* **run:** survive blocker-less issues in parse_blockers ([ab11942](https://github.com/jordansmall/spindrift/commit/ab119424fb523f4dd22ddd9e8371945d8ce52c30))
* **schema:** regenerate harness.env.example after adding MAX_REBASE_ATTEMPTS ([dca1052](https://github.com/jordansmall/spindrift/commit/dca1052c696b6a22a59aacd563ef7eab171a0291))
* **schema:** regenerate harness.env.example after rebase onto main ([67365ff](https://github.com/jordansmall/spindrift/commit/67365ffb5e4efa7f14169a18eba5352e9e869104))
* **test:** guard FAKE_CLAUDE_RESOLVE_CONFLICT git ops behind rebase-in-progress check ([5f76d29](https://github.com/jordansmall/spindrift/commit/5f76d299d421e63ed3006f8916641c5de0a809d8))
* **test:** install fake git only in merge-gate conflict tests ([e8ddb6e](https://github.com/jordansmall/spindrift/commit/e8ddb6e680e99a3025eea6d88177b5bb24866f8a))
* **test:** make bwrap fan-out test deterministic ([02bef2a](https://github.com/jordansmall/spindrift/commit/02bef2ad02f3fbc98e22c045e68ae3f1fe941fa5)), closes [#168](https://github.com/jordansmall/spindrift/issues/168)
* **tests:** seed bare repo HEAD on main so seed_flake_repo can push ([a705cf1](https://github.com/jordansmall/spindrift/commit/a705cf1ba24ae828d3975d2d43384fef48841267))
* **test:** update PR-adoption test for exit 2 on empty queue ([03e10de](https://github.com/jordansmall/spindrift/commit/03e10de790c311951b6a967554ca500695037de7))
* **test:** use real git via setup_bare_repo for rebase bats tests ([c7f7be8](https://github.com/jordansmall/spindrift/commit/c7f7be89614c8af994c763f08a010a5690f0c928))
* **usage:** attribute roles via subagent_type, not position ([3543e47](https://github.com/jordansmall/spindrift/commit/3543e47bc064d93d73591f56b5f2c611472b3ea2))


### Performance Improvements

* **checks:** inspect only the customisation layer in nix-conf-in-image ([1124b1e](https://github.com/jordansmall/spindrift/commit/1124b1e9700c4085d27c8d1ea143cb1f21303ff0))


### Security

* **build:** pin nixBuilderImage by digest ([91e6cb0](https://github.com/jordansmall/spindrift/commit/91e6cb03c100b808d45d63671126199b50650502))
* **oci:** add hardening flags to agent container ([5c561b8](https://github.com/jordansmall/spindrift/commit/5c561b87db0448c4a273e6b57b411099db833961))
* **run:** keep secrets off bwrap argv ([29063c0](https://github.com/jordansmall/spindrift/commit/29063c0b2fb09c17e63d694929396877d4ec6898))
* **run:** restrict agent egress via network knobs ([95be6f5](https://github.com/jordansmall/spindrift/commit/95be6f5f223d476bb59252208b541b5d6847e3eb))
* **run:** wire PIDS_LIMIT and MEMORY_LIMIT into OCI adapter ([6268498](https://github.com/jordansmall/spindrift/commit/6268498bd89983ed0304e8520bfe802d243343cc))
* **schema:** add PIDS_LIMIT and MEMORY_LIMIT knobs ([429d48e](https://github.com/jordansmall/spindrift/commit/429d48eaada5df38774ed3eb16b5b826dab656d6))


### Reverts

* "feat(run): port launcher to nix-built Go binary ([#89](https://github.com/jordansmall/spindrift/issues/89))" ([ee1db3b](https://github.com/jordansmall/spindrift/commit/ee1db3b58f90cef2daf281bfcf1e005b268f23cd))


### Documentation

* add harness domain glossary ([2a487f4](https://github.com/jordansmall/spindrift/commit/2a487f4c2519b9ad99870038f3cdb4d7d4abe575))
* add project CLAUDE.md with agent label lifecycle ([1ce2393](https://github.com/jordansmall/spindrift/commit/1ce23934d1580ec694653387b3e19f0497fd3744))
* add readme ([3622b72](https://github.com/jordansmall/spindrift/commit/3622b72f0f069fcf247bdb9cf172fa649dc05dce))
* add threat model section to README ([ee46b4b](https://github.com/jordansmall/spindrift/commit/ee46b4bfde0f06f6a15c8a7c3e5c41c9be1f3ac6))
* **adr:** add ADR-0009 pluggable Driver seam; glossary Driver/Provider ([369144b](https://github.com/jordansmall/spindrift/commit/369144bad21ff7a503591720ca813b4c69d88463))
* **adr:** add ADR-0010 consumer CLI surface; reconcile image naming ([c46e849](https://github.com/jordansmall/spindrift/commit/c46e84904b3213acad5abada7381d2b55c21609b))
* **adr:** add ADR-0011 selective list dispatch; correct engage in ADR-0010 ([2df3ff2](https://github.com/jordansmall/spindrift/commit/2df3ff2de333faa40ac8419c0e7331da27c9e695))
* **adr:** add ADR-0012 MERGE_MODE and agent-complete decoupling ([18c5f62](https://github.com/jordansmall/spindrift/commit/18c5f62dfff2ad1a88b0290709822a09bd0a7261))
* **adr:** build realizes image, container fallback ([e458919](https://github.com/jordansmall/spindrift/commit/e458919503c72c98982369cbab07876fd57b8697))
* **adr:** nix computes, generated bash executes ([f2e5953](https://github.com/jordansmall/spindrift/commit/f2e5953634a0989d8fbe6e6a78fa2c88e9bcf2b1))
* **adr:** record 0007 — Go orchestration, thin bash glue ([4c0de2a](https://github.com/jordansmall/spindrift/commit/4c0de2a0e1808d078d0a271a2d9db54d452656dd))
* **adr:** record 0008 — nix is a first-class default in the box ([6dc9278](https://github.com/jordansmall/spindrift/commit/6dc9278eec35c8986fcdb64e711e795484a7922b))
* **adr:** record pluggable Box runner with daemonless bwrap ([de91382](https://github.com/jordansmall/spindrift/commit/de9138293a6f2edc97f2a0b3549311d043c8d07d))
* align README and ADR 0003 with the Go template ([09e8c2d](https://github.com/jordansmall/spindrift/commit/09e8c2df03c3e0e59e9172d2139f435a5f07ab0e)), closes [#171](https://github.com/jordansmall/spindrift/issues/171)
* **checks:** explain SKILLS_AGENT_FILES bats dependency ([506ee54](https://github.com/jordansmall/spindrift/commit/506ee545a0ae80718c7de190c979d1d5e6f9a13d))
* **context:** add Forge glossary entry ([146d3a2](https://github.com/jordansmall/spindrift/commit/146d3a2ef1dfbb769d45e9afb445978716c9435e))
* **context:** add label lifecycle entry ([d817259](https://github.com/jordansmall/spindrift/commit/d8172598d0125f81b8f934801484f6c158e19481))
* correct prompt-is-baked drift ([4919a1b](https://github.com/jordansmall/spindrift/commit/4919a1b326c4b8cf9a253ed82871a1588b5762c3)), closes [#67](https://github.com/jordansmall/spindrift/issues/67)
* document the label lifecycle ([db4c6e8](https://github.com/jordansmall/spindrift/commit/db4c6e8c23410a1b2962a65f0f535b33c5bf95c9))
* **launcher:** clarify ensureImage branch-3 reachability ([87242d1](https://github.com/jordansmall/spindrift/commit/87242d159bd6986010a87664bd02ced726d0d184))
* **launcher:** document [#140](https://github.com/jordansmall/spindrift/issues/140) dependency in runWithRetry ([3e49312](https://github.com/jordansmall/spindrift/commit/3e49312022a24d4be7c5f73c66cc671cdaf4f0ea))
* **nix:** fix vendorHash example — bind pkgs via import ([d05a9b6](https://github.com/jordansmall/spindrift/commit/d05a9b6b4cbb6076ea8ebe05e5089669e74d6b3b))
* nudge GH_TOKEN error toward a scoped PAT ([1f0a51f](https://github.com/jordansmall/spindrift/commit/1f0a51f4b2b713ca267d59cce6921d67e41a2bb6))
* **prompt:** move merge+label to launcher; agent emits status=ready ([98c121b](https://github.com/jordansmall/spindrift/commit/98c121b7fddc4b444cc2fca24a9cf0415330461f))
* **readme:** add License section ([1901af1](https://github.com/jordansmall/spindrift/commit/1901af1754bb3a9aff39c137f12f6450c7bb38bc))
* **readme:** add MIT license badge to header ([ff51d8f](https://github.com/jordansmall/spindrift/commit/ff51d8f19cb067e27a98762687e53660b0912d9d)), closes [#258](https://github.com/jordansmall/spindrift/issues/258)
* **readme:** describe realize-then-load and the fallback ([46955b7](https://github.com/jordansmall/spindrift/commit/46955b7cc31d5d2afa4a1cf4455b15aae77bbcf6))
* **readme:** example for devShell-toolchain Target repos ([757381a](https://github.com/jordansmall/spindrift/commit/757381a062d633f9b78de502c1d23e70d9d8b7ed)), closes [#271](https://github.com/jordansmall/spindrift/issues/271)
* **readme:** sync with the Go launcher and merge-gate architecture ([67afec0](https://github.com/jordansmall/spindrift/commit/67afec0bbaf8cb0660cf8bccd65a5e92cf6d2a78))
* record nix-module refactor decisions ([cf34c2f](https://github.com/jordansmall/spindrift/commit/cf34c2fa5ca13d781048aed1d6b81a3ad894b7f0))
* rewrite README for the consumed model ([6da70e1](https://github.com/jordansmall/spindrift/commit/6da70e1bf453b593922a354ddbc0ed6e6cfed95c))
* **run:** update parseBlockerRefs comment to match fix ([101649b](https://github.com/jordansmall/spindrift/commit/101649b0ce3f50f53597b098255809da3209edc1))
* **security:** document deployment security model ([c1b9ba4](https://github.com/jordansmall/spindrift/commit/c1b9ba40535ac07ab15ac169ddef581186ca763c))
* token scope + threat model for self-merge ([7196303](https://github.com/jordansmall/spindrift/commit/71963031fee55f1897ef42f38a6845a4e0b6f807))
* trim comments in default template ([ea33d62](https://github.com/jordansmall/spindrift/commit/ea33d62c828ef13d24e3c24a4fccdd94d113f4f5))
* trim comments in shell scripts and module ([1a26a07](https://github.com/jordansmall/spindrift/commit/1a26a0784f3c8cfdf87795c4bd6053df5e10edbd))
* trim superfluous comments in flake.nix ([cdfc213](https://github.com/jordansmall/spindrift/commit/cdfc213a9ae4f520bab261edc39ebca3d6c4a6bc))
* trim superfluous comments in mkHarness ([ba0e85a](https://github.com/jordansmall/spindrift/commit/ba0e85a63adca6f1aa4b0a5b55900504fa93dca7))
* trim superfluous comments in tests ([f398249](https://github.com/jordansmall/spindrift/commit/f398249e5844e835490f6e325bc312cc8bffa518))


### Code Refactoring

* **dogfood:** replace gh probe with launcher exit codes ([b842b74](https://github.com/jordansmall/spindrift/commit/b842b74c5ef5f38c16c6d82d1df1693dbc85be08))
* **flake:** convert dogfood harness from Rust to Go ([9d2b518](https://github.com/jordansmall/spindrift/commit/9d2b51842bc5abe6146d8d55990f6f7153248c42)), closes [#171](https://github.com/jordansmall/spindrift/issues/171)
* generate launchers via writeShellApplication ([a12cd39](https://github.com/jordansmall/spindrift/commit/a12cd39e8cce36259ad093bdafc181220bd3ee76))
* **launcher:** extract drainMaxJobs from run() ([69ba99c](https://github.com/jordansmall/spindrift/commit/69ba99cfb429339f4c6ebb53d363c037cabca091))
* **launcher:** route all gh calls through forge.Client ([d7aa057](https://github.com/jordansmall/spindrift/commit/d7aa0579221a93b5997713515510510bffd54e54))
* **launcher:** wire printOutcomeReport to outcome pkg ([54f777e](https://github.com/jordansmall/spindrift/commit/54f777e9d3ecf5d1ff28c28a1ff7b186d816f4b2))
* **launcher:** wire runner seam into main + add launcher build subcommand ([ce03be9](https://github.com/jordansmall/spindrift/commit/ce03be98ce7be9815a5a2d87795b38797293f2c8))
* **nix+tests:** delete bash build scripts, collapse preamble, wire Go build ([401b550](https://github.com/jordansmall/spindrift/commit/401b550f5f4daf5a3b83ee6b00f0b59897c63a20))
* **nix:** drop unused renderFlagTableGo binding ([8c9bf06](https://github.com/jordansmall/spindrift/commit/8c9bf06c1b27e14e43a81d0932594e60c908f128))
* **nix:** env schema registry ([acc5b6a](https://github.com/jordansmall/spindrift/commit/acc5b6a9adf68a911d3b38a7e99d3408abd75183))
* **nix:** split flake.nix into fixtures and checks modules ([3213f59](https://github.com/jordansmall/spindrift/commit/3213f5965c281c5dd782a241884eaf4e9617dfcc)), closes [#107](https://github.com/jordansmall/spindrift/issues/107)
* **prompts:** tighten container prompts, drop prose ([e4ca5ab](https://github.com/jordansmall/spindrift/commit/e4ca5abbf908cd2c576ce72e1c4b3c1c92db8d7f)), closes [#164](https://github.com/jordansmall/spindrift/issues/164)
* relocate scaffold under templates/default ([872a052](https://github.com/jordansmall/spindrift/commit/872a052289bd0834522c30296c329b4d237746f1))
* **schema:** move devShellProbeTimeout to Consumer-tunable section ([07705d4](https://github.com/jordansmall/spindrift/commit/07705d4932c5b930a3a8f75421c3810e141d6dd4))
* **template:** switch example toolchain to Go ([8d63559](https://github.com/jordansmall/spindrift/commit/8d63559cf64d8e32146e78bdb5aa4d4131571c2e)), closes [#171](https://github.com/jordansmall/spindrift/issues/171)
* **tests:** drop brittle prompt-prose grep tests ([25599fa](https://github.com/jordansmall/spindrift/commit/25599fa1b8db837cc42c6e94c075626af7fedadc))
* **tests:** unify three runtime fakes into one ([8f72b20](https://github.com/jordansmall/spindrift/commit/8f72b200d9dfdef5b977d7fa3a041b51951fc085))


### Tests

* add shared bats harness with tool fakes ([f2e7172](https://github.com/jordansmall/spindrift/commit/f2e7172c8c4a70202989e12035176a5ed796eb4b))
* **box:** add hang probe fake and timeout bats test ([33c8bbf](https://github.com/jordansmall/spindrift/commit/33c8bbfd224d545c754a5749e446ce42edea92ec))
* **build:** add error-surfacing and artifact-staging coverage ([c724345](https://github.com/jordansmall/spindrift/commit/c7243454c7f21298832e5aadfb1d1268fcb7a07a))
* **build:** cover realize, fallback, and both-fail paths ([22fab3c](https://github.com/jordansmall/spindrift/commit/22fab3c0cbb7f34f8646305d6709e77af973b6cd))
* **check:** assert the baked entrypoint has a store shebang ([157f0d5](https://github.com/jordansmall/spindrift/commit/157f0d51f6c20ec6847db80c8b3966f2e8192491))
* **checks:** assert nix baked by default; verify lean escape hatch ([52bce19](https://github.com/jordansmall/spindrift/commit/52bce19252772fe63a25b8e71dd240a2f8022929))
* **classify:** cover session-limit resetsAt propagation ([c6f9ee5](https://github.com/jordansmall/spindrift/commit/c6f9ee513623991a39ff48f7fb40a2ed6d8d092c))
* cover prefetch hook and language-agnostic engine ([6ad7efa](https://github.com/jordansmall/spindrift/commit/6ad7efa2ab2af9c1c77b71e20181d0740d7e73ff))
* cover run knobs (baked defaults + docker runtime) ([bd1ab22](https://github.com/jordansmall/spindrift/commit/bd1ab221070bdc5d2691a34fb4f7262cb7a4cf5b))
* cover the label lifecycle ([46c9bdf](https://github.com/jordansmall/spindrift/commit/46c9bdfec3149855837c5ca7b3f6ed55f2612a26))
* drive bats through fake-gh overlay harnesses ([0261303](https://github.com/jordansmall/spindrift/commit/0261303a35e4324a47b3b3061af9e8471d79b34c))
* **entrypoint:** assert prompt uses --force-with-lease ([e8e4c3c](https://github.com/jordansmall/spindrift/commit/e8e4c3cd6199324828ab5e76058cfc34061ec7fc))
* **entrypoint:** cover pre-work rebase scenarios ([c151357](https://github.com/jordansmall/spindrift/commit/c151357c801166f59020aa51d5c8b9232fe0c396))
* **entrypoint:** cover skill-present and skill-absent prompt paths ([73e204c](https://github.com/jordansmall/spindrift/commit/73e204caa49e28b7c8fbd654c7990b227bff0be5))
* **entrypoint:** match --agents JSON payload, not the prompt's word ([36eb897](https://github.com/jordansmall/spindrift/commit/36eb897678677cdb1917c1f1fbbeef60ac6ef46f))
* **entrypoint:** verify skill discovery via HOME/.claude/skills ([7ba0530](https://github.com/jordansmall/spindrift/commit/7ba0530435bdc13364c88175aa97e8ba8b119d67))
* **fakes/gh:** add pr view support and replace awk with bash loop ([6b4cf6d](https://github.com/jordansmall/spindrift/commit/6b4cf6d84ed487e42b81b723d61a8f43e5ad1d2a))
* **fakes:** extend nix fake with develop case; add seed_flake_repo ([138b330](https://github.com/jordansmall/spindrift/commit/138b3305beb415a8f2911e720951c93ac11fcc4b))
* **fanout:** cover failing-container slot-release scenario ([77f8055](https://github.com/jordansmall/spindrift/commit/77f805506a0361158a877bd0fa48b131c28cf715)), closes [#126](https://github.com/jordansmall/spindrift/issues/126)
* **forge:** add CheckState error scripting to fake ([a6b1226](https://github.com/jordansmall/spindrift/commit/a6b122689562c0cc8c7fb20846e600485ffda6d6))
* **format-transcript:** fix truncation bound in test name ([147425f](https://github.com/jordansmall/spindrift/commit/147425f2d663af496f00ae55d5acad2fd1e9b32e))
* **heartbeat:** add failing test for count-line emission ([1ba5b2b](https://github.com/jordansmall/spindrift/commit/1ba5b2bd24cb890767f4ad35b3908f196e2de5c3))
* **heartbeat:** cover narration emission and subagent drop ([20a5955](https://github.com/jordansmall/spindrift/commit/20a5955d8ca9b675f93ace6ab5db9dbefa44b70d))
* **heartbeat:** update tests for count-based emission ([443ec8c](https://github.com/jordansmall/spindrift/commit/443ec8c0c87e26b580f83263edb03ab0d4af9575))
* **image:** verify content-hash tag on build and run ([2a819a8](https://github.com/jordansmall/spindrift/commit/2a819a8db8ecc4ff151d38affef1f7f9fa7a308a))
* **launcher:** assert --help output content ([b26a8be](https://github.com/jordansmall/spindrift/commit/b26a8be7147e3581a9b66c6a12f84f5256cf1a15))
* **launcher:** assert claiming gated by maxParallel semaphore ([c22088b](https://github.com/jordansmall/spindrift/commit/c22088b5fecb3fb4a8a527e8eefaed5cb99d644c))
* **launcher:** bats coverage for self-heal fix-agent passes ([fe3fbb6](https://github.com/jordansmall/spindrift/commit/fe3fbb6dc0f22bc713bfa748da350c6e3ce8c67b))
* **launcher:** cover --&lt;name&gt;-file flag parsing and help display ([d7da402](https://github.com/jordansmall/spindrift/commit/d7da40208ffb6c5b462480808962c51addeaa8bc))
* **launcher:** cover alias flag parsing and help display ([efa5805](https://github.com/jordansmall/spindrift/commit/efa58059a55233bb0ff6a8bb8c0746dd021083fc))
* **launcher:** env-parsing edge cases for integer knobs ([7541502](https://github.com/jordansmall/spindrift/commit/75415028999bdf83f5eed588af162fa5f68f88ce))
* **launcher:** hold-until-reset + retry coverage ([db08b7f](https://github.com/jordansmall/spindrift/commit/db08b7f7fc8972bdf0e4a2c2e2f59872e967ceda))
* **launcher:** MAX_JOBS blocked-skip and cycle detection ([9c6a483](https://github.com/jordansmall/spindrift/commit/9c6a483f4fa35cef6c383da5bd3b1d27ae9f6802)), closes [#131](https://github.com/jordansmall/spindrift/issues/131)
* **launcher:** merge gate paths through forge.Fake ([5400398](https://github.com/jordansmall/spindrift/commit/5400398117774bd27eb2fb29e3013f41824ce6a3))
* **launcher:** tighten bats self-heal coverage ([8834edf](https://github.com/jordansmall/spindrift/commit/8834edf653f6f823c5058f3d72f772ecdc0e8b89))
* **prompt:** assert baked prompt, no default mount ([fe4c551](https://github.com/jordansmall/spindrift/commit/fe4c5510951b3199ca00a53246b868d95d2dae31)), closes [#63](https://github.com/jordansmall/spindrift/issues/63)
* **prompt:** assert WATCH CI uses statusCheckRollup ([6d48638](https://github.com/jordansmall/spindrift/commit/6d486380b027fa1a91893f6a6fd95c3ffa995352))
* **prompt:** fall back to source tree outside nix harness ([42de0d3](https://github.com/jordansmall/spindrift/commit/42de0d3a35d4f656d33d7557d896ce0e8ffc0beb))
* prove skill discovery via skill-aware fake claude ([262dc7f](https://github.com/jordansmall/spindrift/commit/262dc7f51bb3a9545b49824bec4bd3a86ecb5e0c))
* **run:** align bwrap outcome test with merged-state verification ([d165e7a](https://github.com/jordansmall/spindrift/commit/d165e7aa8e2dc2093b9eb0d064a92d703eb9ec08))
* **run:** assert bwrap secrets not on argv ([65ff1f9](https://github.com/jordansmall/spindrift/commit/65ff1f988caf5990de1dc23e4157b6fa7b1ce470))
* **run:** bats coverage for conflict→rebase→merge gate ([1a42403](https://github.com/jordansmall/spindrift/commit/1a42403365d141e7668cad62ad3848d6ae24b95e))
* **run:** bats coverage for engage subcommand ([50e380e](https://github.com/jordansmall/spindrift/commit/50e380e0d0ab3e1267a6346ef664d22ef4e6edf5))
* **run:** bats coverage for OCI security hardening flags ([6b3feed](https://github.com/jordansmall/spindrift/commit/6b3feedba3c0a4e412480603312e58c35faed7af))
* **run:** bats coverage for stranded-issue reconcile on startup ([89dd75f](https://github.com/jordansmall/spindrift/commit/89dd75fec5224d32e71b9e7328ec2abff464e614))
* **run:** bats integration tests for async check registration ([3e27bd8](https://github.com/jordansmall/spindrift/commit/3e27bd852f2c7f3841e59a79bd002ce753f2ae33))
* **run:** bats regression coverage for launcher fixes [#91](https://github.com/jordansmall/spindrift/issues/91) [#93](https://github.com/jordansmall/spindrift/issues/93) [#94](https://github.com/jordansmall/spindrift/issues/94) ([d57f1b8](https://github.com/jordansmall/spindrift/commit/d57f1b8bb14e99176981943d6449c8e91221e7e3))
* **run:** clarify bwrap default netns test title ([5be16a5](https://github.com/jordansmall/spindrift/commit/5be16a5637c1b4bb5e8b73b860cdfdbd4e41a574))
* **run:** clarify timeout and set explicit timeout in gate tests ([7266ff4](https://github.com/jordansmall/spindrift/commit/7266ff42b5c8f33945b9879e44b490d4951dc24e)), closes [#130](https://github.com/jordansmall/spindrift/issues/130)
* **run:** cover BARRIER_LABEL fence and off-by-default path ([4dfbfc8](https://github.com/jordansmall/spindrift/commit/4dfbfc801a457ff186b7f2435d1d673c27f1c637))
* **run:** cover pending/absent CI refusal in merge gate ([129f807](https://github.com/jordansmall/spindrift/commit/129f8073d4fd1e6f881b2d18718f2d4634b0b823))
* **run:** cover PR adoption when outcome line is missing ([c414d35](https://github.com/jordansmall/spindrift/commit/c414d357644157bf089d9685c5b246101306fbf9))
* **run:** cover red-CI refusal in merge gate ([4ea9465](https://github.com/jordansmall/spindrift/commit/4ea94653e2bce63b8d3a0a0bea78cad4e368c8f9))
* **run:** cover rollup-state merge gate paths ([7ef28ae](https://github.com/jordansmall/spindrift/commit/7ef28aeeac5be58149c2be024d01f8e338f1a6f8))
* **run:** cover skipping-merges and mixed-pending refusal ([3085dc6](https://github.com/jordansmall/spindrift/commit/3085dc637dc14914fa2f33c94e01119ed4d7b1f2))
* **run:** merge-gate conflict→rebase unit tests ([90da009](https://github.com/jordansmall/spindrift/commit/90da009c331cd57d01ccbfb5cc609fdcd73c12be))
* **runner:** add isDigestPinned helper and tests ([b40aa7b](https://github.com/jordansmall/spindrift/commit/b40aa7b44e141dd0f38808ceed498ec2b2fc5268))
* **run:** remove dep-wave tests; update prompt-mount check for Go binary ([71dd3f2](https://github.com/jordansmall/spindrift/commit/71dd3f2a21cce5ba955ab6b64020d2728eeb6d3c))
* **run:** remove dep-wave tests; update prompt-mount check for Go binary ([4464b76](https://github.com/jordansmall/spindrift/commit/4464b7604042ae595d7399f80337d7355f043a10))
* **run:** remove duplicate queue-empty test ([fa77fee](https://github.com/jordansmall/spindrift/commit/fa77fee16043344e34baa6aa11f671e3d528295e))
* **run:** restore dep-wave bats tests; extend gh fake ([41080fc](https://github.com/jordansmall/spindrift/commit/41080fc83c5f51ee2b062fe1b6f523d75940ed6d))
* **run:** unit coverage for engageByNumber ([0189c75](https://github.com/jordansmall/spindrift/commit/0189c758ced1bc92a70b0eb9719a69743b431464))


### Build System

* add nix flake for the agent container image ([927397d](https://github.com/jordansmall/spindrift/commit/927397d1a45512ffb569e3f808d7c0f033688220))


### Continuous Integration

* add release-please workflow ([482f8eb](https://github.com/jordansmall/spindrift/commit/482f8ebc6a3db10a238e3b6c54b46884eb33168b))
* **agent-dispatch:** claim the issue before building ([89b622e](https://github.com/jordansmall/spindrift/commit/89b622e65b37faf18ea464af1c905fd6ca8b34bc)), closes [#152](https://github.com/jordansmall/spindrift/issues/152)
* **agent-dispatch:** claim the issue right after checkout ([af2836b](https://github.com/jordansmall/spindrift/commit/af2836beb40e177933a0e3ce7383c221082c9eba)), closes [#178](https://github.com/jordansmall/spindrift/issues/178)
* **agent-dispatch:** key concurrency per issue ([4a97fcd](https://github.com/jordansmall/spindrift/commit/4a97fcd75aeabcfb9223ac070024bc798c66ec8b))
* dispatch a dogfood run when agent-trigger label is added ([b3a0435](https://github.com/jordansmall/spindrift/commit/b3a043541db523af16fcd4d2377da59e0093298f))
* **nix:** Go toolchain checks for the launcher ([5059c32](https://github.com/jordansmall/spindrift/commit/5059c32112b5e74595ca70f6f1818f60801f90a6)), closes [#112](https://github.com/jordansmall/spindrift/issues/112)
* revert CI and agent-dispatch to the ubuntu-latest runner ([55d52ed](https://github.com/jordansmall/spindrift/commit/55d52ed8595d270a31fc83f5b7260b35a53d473a))
* run CI and agent-dispatch on the self-hosted macbook-air runner ([bbe6cd2](https://github.com/jordansmall/spindrift/commit/bbe6cd29594c682cba5b01c81238b9ac55219b9d)), closes [#234](https://github.com/jordansmall/spindrift/issues/234) [#235](https://github.com/jordansmall/spindrift/issues/235)
* validate flake check and image build on PRs ([2374c58](https://github.com/jordansmall/spindrift/commit/2374c589c677f701bbaf453948b4dc1217563c88))


### Miscellaneous Chores

* add gitignore ([08255e5](https://github.com/jordansmall/spindrift/commit/08255e5561a71cdac15fb4623a78ca36c3423d6e))
* add MIT LICENSE file ([1d0e3df](https://github.com/jordansmall/spindrift/commit/1d0e3df0a411fa4c4606014e59e6b26e755be1ff))
* add release-please manifest mode config ([8aa82a4](https://github.com/jordansmall/spindrift/commit/8aa82a42761aefac8ed49441b21b05fbdd92865d))
* **dogfood:** add dogfood.sh to shellcheck quality gate ([7f4fed9](https://github.com/jordansmall/spindrift/commit/7f4fed9d6174beb343451142a384b86a85ce5c0f))
* **launcher:** remove unused model/scoutModel/reviewModel ([9cc0d9b](https://github.com/jordansmall/spindrift/commit/9cc0d9ba17bf1b08f327d58d3306f0bcf8275c12)), closes [#157](https://github.com/jordansmall/spindrift/issues/157)
* **nix:** delete dead bash run launcher ([078ecc6](https://github.com/jordansmall/spindrift/commit/078ecc6a5c09c524d8ef551fd4d982a1b7e34bdb))
* **release-please:** configure explicit changelog sections ([4231f96](https://github.com/jordansmall/spindrift/commit/4231f96a762c0e7241a28eed0a1e1d34284e09f7))
* **schema:** add SPINDRIFT_SKILLS_DIR to harness.env.example ([fe58550](https://github.com/jordansmall/spindrift/commit/fe58550aa3dba0de7523279257159b23791ca4bd))
* **template:** add BARRIER_LABEL to harness.env.example ([791977a](https://github.com/jordansmall/spindrift/commit/791977a6f8ee95fd495aba9fff16d02380203f38))
* **template:** add build artifacts to .gitignore ([c7b77a3](https://github.com/jordansmall/spindrift/commit/c7b77a39e1c82cf176881e9c9e4f1fd88c7fc20f))
* wire manifest version into mkHarness; add VERSIONING.md ([eaa71a4](https://github.com/jordansmall/spindrift/commit/eaa71a430774528156e12cfb012a60a91000abc2))
