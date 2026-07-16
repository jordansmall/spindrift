# Changelog

## [0.5.0](https://github.com/jordansmall/spindrift/compare/v0.4.2...v0.5.0) (2026-07-16)


### ⚠ BREAKING CHANGES

* **nix:** remove deprecated run/build app aliases

### Features

* **completion:** complete enumerable flag values ([0a2cffd](https://github.com/jordansmall/spindrift/commit/0a2cffd4e45107909a7e9b1799ef3da965a59004))
* **console:** add control-sequence sanitize helper ([3111d1e](https://github.com/jordansmall/spindrift/commit/3111d1ea27666f2215719edf6c0ba5fe0f0e77e2)), closes [#721](https://github.com/jordansmall/spindrift/issues/721)
* **console:** add drill-in transcript view with raw toggle ([dae2cdb](https://github.com/jordansmall/spindrift/commit/dae2cdb5b3af0c75537d8e07037aa5fb5793fe9e))
* **console:** add header status line, drop bare cap line ([f67788e](https://github.com/jordansmall/spindrift/commit/f67788e03bca2b04837071b29b149fdd00d084ef))
* **console:** add Launcher.Rebuild in-session action ([2414571](https://github.com/jordansmall/spindrift/commit/241457198f68ba65184dee4c3b42e47cd57bdf1f))
* **console:** add Launcher.Terminate action ([3248bd0](https://github.com/jordansmall/spindrift/commit/3248bd051f41fcb5e688491445fbf5069791f50f))
* **console:** add per-column focus and cursors to Update ([75d503f](https://github.com/jordansmall/spindrift/commit/75d503f1459edc8ed9bae408846299b86ab51254))
* **console:** add Pick/Unpick to the model core ([e80785c](https://github.com/jordansmall/spindrift/commit/e80785c1fa42ba3f6879130e59cece50dea2fd74))
* **console:** add queueSettler to mark a pick settled ([825fe2a](https://github.com/jordansmall/spindrift/commit/825fe2a18f561c6cec01e5f689d05e88f94f115d))
* **console:** add spindrift banner, collapse on short terminal ([c9c3fc5](https://github.com/jordansmall/spindrift/commit/c9c3fc54dd8b78dae13fdf2111edc1fb5e6615a7))
* **console:** add the dogfood pid-file startup check ([8bb4f2f](https://github.com/jordansmall/spindrift/commit/8bb4f2f4b79300e760efd57ce4f5826320d3ab10))
* **console:** add the Elm-architecture model/update/view core ([c3a0f7b](https://github.com/jordansmall/spindrift/commit/c3a0f7b7d63652a0dac00610a3bcec14e118f8f4))
* **console:** add the IssueTracker refresh adapter ([fe4501a](https://github.com/jordansmall/spindrift/commit/fe4501a4f5a19477506e9de512f73d57cabd39ef))
* **console:** add the operator-queue Discoverer ([4b78d5f](https://github.com/jordansmall/spindrift/commit/4b78d5f4b7daf5d28bdbb127eb649983f662434d))
* **console:** add the PickIssue adapter ([3a37ec9](https://github.com/jordansmall/spindrift/commit/3a37ec917fd5625f5dd7a9bd1c634b7a9bed4f7b))
* **console:** add the read-render command loop ([1a70275](https://github.com/jordansmall/spindrift/commit/1a702753badfed3dd14d6792254f38031ab0bdb6))
* **console:** add Transcript pane mode to Model ([0364d51](https://github.com/jordansmall/spindrift/commit/0364d511c519c7ba0054c222c447245d1679ee6f))
* **console:** bind "+"/"-" to the live parallelism cap ([f922e8d](https://github.com/jordansmall/spindrift/commit/f922e8d2fb7e4e61378ad8e9c627a1470efabff7))
* **console:** bind pgup/pgdown to scroll the body ([7258d11](https://github.com/jordansmall/spindrift/commit/7258d11e5b4f1585a351f4b3f1f90818c5b97eb1))
* **console:** drain-by-default quit and orphan recovery ([10e2192](https://github.com/jordansmall/spindrift/commit/10e2192a870f87ae8046aebed10a402263b09c09))
* **console:** drive the console with a real Bubble Tea program ([eec79fe](https://github.com/jordansmall/spindrift/commit/eec79fec2e723d13ac107b77eef778e79de76453))
* **console:** gate refills on a stale freshness checker ([7596f6b](https://github.com/jordansmall/spindrift/commit/7596f6b2711024738499367ae489cfa9a09eb343))
* **console:** give the model cursor, filter-edit, and help state ([638caa0](https://github.com/jordansmall/spindrift/commit/638caa0110a1a69dc52662a36255349c453d6f4d))
* **console:** hold picks on open blockers, launch when clear ([a328ad9](https://github.com/jordansmall/spindrift/commit/a328ad9a652d887c06a328760fe2377876d75105))
* **console:** parallel slots, pick-all-ready, freshness triggers ([cc2180d](https://github.com/jordansmall/spindrift/commit/cc2180d8aef1d8508f7ebf96ea9355ef2bb30254)), closes [#647](https://github.com/jordansmall/spindrift/issues/647)
* **console:** pin header, window body by height ([3948876](https://github.com/jordansmall/spindrift/commit/3948876a92627c0f61e1b18819b21f5d05d74446)), closes [#1035](https://github.com/jordansmall/spindrift/issues/1035)
* **console:** relocate stale/dogfood alerts into header ([a10f691](https://github.com/jordansmall/spindrift/commit/a10f691832ee039554c3ef9bc13b9dd701f78634))
* **console:** render docked and floating Transcript panes ([75b2231](https://github.com/jordansmall/spindrift/commit/75b2231682f0738b7317fa5dcd9184931481d2d7))
* **console:** render focus marker and queue cursor ([60c9376](https://github.com/jordansmall/spindrift/commit/60c9376a2612db42540110a720c27f233a2d12eb))
* **console:** render stale-image banner state ([c1157a1](https://github.com/jordansmall/spindrift/commit/c1157a1d8ee7d33574f06575505229efacca026b))
* **console:** render the picks queue in View ([f60ae2a](https://github.com/jordansmall/spindrift/commit/f60ae2a62e0bbdeb8f4599d1d8402062c2eafe01))
* **console:** scroll offset follows the cursor ([f6d8829](https://github.com/jordansmall/spindrift/commit/f6d88291a60ffc862733d5bed076a74f3599ddf0))
* **console:** show a position indicator on body columns ([97a16c3](https://github.com/jordansmall/spindrift/commit/97a16c3cecb7b945b3fc7b365ca83fc99a7e69fe))
* **console:** show the live parallelism cap and count ([9b0add0](https://github.com/jordansmall/spindrift/commit/9b0add008bbc7c05d9582f27bb29576025b97ac0))
* **console:** split body into backlog/queue columns ([7bffbf1](https://github.com/jordansmall/spindrift/commit/7bffbf1c2345057d761ed15f9093e476b1f15d23))
* **console:** surface a failed promotion as a dissolved row ([e22751c](https://github.com/jordansmall/spindrift/commit/e22751c17c0c4f56b127f78ea00d85354b34a8bc))
* **console:** track terminal size on Model ([1f378ca](https://github.com/jordansmall/spindrift/commit/1f378caac7d8142694de6c5ff82835b9c213f2d3))
* **console:** translate WindowSizeMsg into SizeChangedMsg ([3989a6d](https://github.com/jordansmall/spindrift/commit/3989a6d709fb5e99cab5328426d27df3c79944ff))
* **console:** wire drill-in transcript pane keybindings ([882aae2](https://github.com/jordansmall/spindrift/commit/882aae27ece0b538d9ac1bc0888cc40a3fdd0db9)), closes [#786](https://github.com/jordansmall/spindrift/issues/786)
* **console:** wire pgup/pgdown to the dynamic page size ([1f964bd](https://github.com/jordansmall/spindrift/commit/1f964bda7a4c8e61a03497c9e48fbc71cb5e9860))
* **console:** wire pick and unpick commands into the run loop ([15a84d9](https://github.com/jordansmall/spindrift/commit/15a84d93a61a4d02f6b8a5addabd8fb566a96c97))
* **console:** wire pick/unpick/terminate/resize/rebuild keys ([80231c7](https://github.com/jordansmall/spindrift/commit/80231c75ab31d4d0e83e13fcd0a8e571f2dd1a31))
* **console:** wire rebuild key and stale banner into Run ([7b912ab](https://github.com/jordansmall/spindrift/commit/7b912ab39ec2f4a664068934f100b39cf273cda9))
* **console:** wire Tab and context-sensitive Enter ([46fda59](https://github.com/jordansmall/spindrift/commit/46fda59b7df3b2112042d9cb225c22f9ed05dcc8))
* **console:** wire terminate confirm and command ([f1a6980](https://github.com/jordansmall/spindrift/commit/f1a6980d980bde1ffa0a4c899dd821877d826c72))
* **console:** wire the console subcommand to actually launch ([2776145](https://github.com/jordansmall/spindrift/commit/27761452bb8a81cfd50e70a42c358e4a2da0ef39))
* **console:** wire the pane-mode cycle key ([6cb7484](https://github.com/jordansmall/spindrift/commit/6cb748423eb841681202838b798f34a8c59cbba1))
* **dispatch:** add Factory.Kill and AppendTerminalLine ([207deb1](https://github.com/jordansmall/spindrift/commit/207deb1d208a5671a4d41b6d47f76f4776ca0548))
* **dispatch:** expose pass-log discovery and Driver getter ([62f2e61](https://github.com/jordansmall/spindrift/commit/62f2e6143d5c93b859f10e3fb14230e7e5a7757e))
* **dispatch:** forward dispatch kind as DISPATCH_KIND ([f701d4f](https://github.com/jordansmall/spindrift/commit/f701d4fca8d0bf03b842f7426b986dcf3e66bd3d))
* **dispatch:** parse orphaned issue numbers from boxes ([4bf9794](https://github.com/jordansmall/spindrift/commit/4bf9794667c8955fbc22a3d01c91b18a7a3c2996))
* **doctor:** add researchLabelMeta for docs drift guard ([2951630](https://github.com/jordansmall/spindrift/commit/2951630dac206407d70d959bcbc76095f24292e0))
* **doctor:** check research labels as advisory tier ([47c83d0](https://github.com/jordansmall/spindrift/commit/47c83d0a58eb7a257df3d1d68ba1e183ff381515))
* **dogfood:** bake code-review skill into image ([2ca6ced](https://github.com/jordansmall/spindrift/commit/2ca6ced9226e45d38db4d626d18ab30a0b911f0f))
* **dogfood:** drive research via DOGFOOD_KIND ([8f11749](https://github.com/jordansmall/spindrift/commit/8f1174934a072ba954f4626da27a86972fde2359))
* **driver-exec:** add in-box Go unit for the Driver pipeline ([693cf62](https://github.com/jordansmall/spindrift/commit/693cf62a4b3d9d1558bebae1b493a02b448304e1))
* **driver:** add claude subpackage for classify/heartbeat/transcript ([39cc70b](https://github.com/jordansmall/spindrift/commit/39cc70b411938fd54a018078cce48b18206057b7))
* **driver:** add transcript rendering to the Driver seam ([bf2aa9c](https://github.com/jordansmall/spindrift/commit/bf2aa9c517dd013d64812680d73c901e390904d2))
* **driver:** grow Driver interface with ExtractUsage ([9ae54fc](https://github.com/jordansmall/spindrift/commit/9ae54fc47782e4f050994d321195382a8dd4096b))
* **drivers/claude:** grant reviewer subagent the Agent tool ([9df05e9](https://github.com/jordansmall/spindrift/commit/9df05e9ac58e14f4ae1b079e789c4cfe4bf98ce8))
* **drivers:** registry validates and renders entries ([5a8fd25](https://github.com/jordansmall/spindrift/commit/5a8fd25ad1d3def38f1f81b0e425a010918cd902))
* **entrypoint:** add research dispatch kind path ([2e441fe](https://github.com/jordansmall/spindrift/commit/2e441fe49d9c6f2498a4f47660b80015e579188a))
* **entrypoint:** call driver-exec once for the Driver run ([42b9b5f](https://github.com/jordansmall/spindrift/commit/42b9b5ff817d1a9510ba591b2b39b7e55532d42e))
* **entrypoint:** drop Driver fallback literals ([5de12a8](https://github.com/jordansmall/spindrift/commit/5de12a80cacd3237e81460a5787fe1d5a04ee0cb))
* **entrypoint:** gate a code-review-default fragment ([264ddb3](https://github.com/jordansmall/spindrift/commit/264ddb3adfa209843baf6eefa15297273aaf1ec5))
* **filer:** dedup findings against all open issues ([9427e6d](https://github.com/jordansmall/spindrift/commit/9427e6d1a35c914973372cdf0be0a612054bf36f)), closes [#908](https://github.com/jordansmall/spindrift/issues/908)
* **forge:** add IssueTracker.ListOpenIssues ([368cdb5](https://github.com/jordansmall/spindrift/commit/368cdb5f3b1d36f38bb6ee549c9783d00bb3df4b))
* **forge:** add one PR-for-issue resolver ([f61fed1](https://github.com/jordansmall/spindrift/commit/f61fed152e0171a7946aa7ddd339e6376f1867d1))
* **forge:** add the Untriaged dispatch state ([70aeb4d](https://github.com/jordansmall/spindrift/commit/70aeb4db8f280d315f92aa388b5582b4b5998f7b))
* **forge:** add Verdict and kind-aware CompleteVerdict seam ([76fca9d](https://github.com/jordansmall/spindrift/commit/76fca9d11493d4ca8d6e71fa88aeb3bb937ff37f))
* **forge:** detect a PR behind its base via mergeStateStatus ([d0afc61](https://github.com/jordansmall/spindrift/commit/d0afc6130797108dfaecf631501720380fdbc64d))
* **forge:** move touch-set extraction behind the Issue Tracker seam ([f32f661](https://github.com/jordansmall/spindrift/commit/f32f661b5bad5f47a302728ebc7051f0588aad72))
* **forge:** resolve GitHub blockers from native deps first ([1b85c0f](https://github.com/jordansmall/spindrift/commit/1b85c0f023beb84bce1dddb9bafe5cc9a497a583)), closes [#608](https://github.com/jordansmall/spindrift/issues/608)
* **forge:** tag DepsOf results with native/body source ([abaccbe](https://github.com/jordansmall/spindrift/commit/abaccbe27ef42b4675c47d43ebfd1d8b10acd9a8))
* **freshness:** expose the fetched base-tip rev on Result ([537188a](https://github.com/jordansmall/spindrift/commit/537188abd93f3300cae4673458a900ccd1d0eb93))
* **launcher:** add research subcommand ([70bb24e](https://github.com/jordansmall/spindrift/commit/70bb24e7b83f0e2e668c1c4389b2688c58b92ef4))
* **launcher:** commit the generated knob-defaults table ([26d784b](https://github.com/jordansmall/spindrift/commit/26d784b5bedf2fd237ca663f4d2c8c716ae66fa0))
* **launcher:** drop hand-written knob defaults from loadConfig ([4723742](https://github.com/jordansmall/spindrift/commit/4723742b44e0c3e2359e1330964c56930ae9da75))
* **launcher:** load the input document; document &lt; flag precedence ([8b9ea59](https://github.com/jordansmall/spindrift/commit/8b9ea59fea8322d20032c3bd210cce2d8cb87514))
* **launcher:** render blocker source in preview and skip notices ([f9eab95](https://github.com/jordansmall/spindrift/commit/f9eab95ec340d4eafc1da79d0740a4d3e70494c6))
* **launcher:** wire orphan recovery into console startup ([2b67d04](https://github.com/jordansmall/spindrift/commit/2b67d04e4b8929b63dedc1f40b9aabc7d3b0ece5))
* **launcher:** wire the console subcommand ([4a6f1fc](https://github.com/jordansmall/spindrift/commit/4a6f1fc33a0d2ff34e2847f4f00d1014cbd5c79c))
* **nix:** add Conditional fragment registry data ([5f5d90e](https://github.com/jordansmall/spindrift/commit/5f5d90e45f698264783b72f326451ac8ab87f4c9))
* **nix:** add renderDefaultsTableGo for launcher knob defaults ([feef5e6](https://github.com/jordansmall/spindrift/commit/feef5e6578595ec1475d5208632e31f7978a4dc1))
* **nix:** bake research-prompt.md into the image ([d80b37e](https://github.com/jordansmall/spindrift/commit/d80b37e29f02dea330d8ce5ba32f7d3c0e093cc6))
* **nix:** remove deprecated run/build app aliases ([1044e2e](https://github.com/jordansmall/spindrift/commit/1044e2e76515a518b4a01c38334364981a922f0e))
* **nix:** render the Conditional fragment registry into the image ([00e423d](https://github.com/jordansmall/spindrift/commit/00e423db9bf260af38e426ab9a7edd96fda5abd8))
* **nix:** render the Launcher input document (ADR 0020) ([da0b69e](https://github.com/jordansmall/spindrift/commit/da0b69e1c76f1fb913318325bd24ca390b872a1a))
* **nix:** wire defaults table into regen and drift check ([544981e](https://github.com/jordansmall/spindrift/commit/544981ef771b8da18049fa6ead3fd1014165b280))
* **outcome:** rename outcome line's pr= field to landing= ([8a093be](https://github.com/jordansmall/spindrift/commit/8a093be22507c7abf71058bb2af929a44658bca9))
* **prompts:** defer IMPLEMENT to /tdd and COMMIT to /commit when baked ([5ce1763](https://github.com/jordansmall/spindrift/commit/5ce17639f78b1ce5e7033538f055ef81b412bbaf))
* **prompts:** tell agent to git add before nix build ([df7945a](https://github.com/jordansmall/spindrift/commit/df7945a30ff58b9fd5fa7d9d6dd21fc653174afe))
* **prompts:** treat a vanished exit marker as failure in CHECK ([2207625](https://github.com/jordansmall/spindrift/commit/22076253cd863118701e032f4fdf31149d2816df)), closes [#713](https://github.com/jordansmall/spindrift/issues/713)
* **runner:** add Kill for force-stopping a running box ([228dceb](https://github.com/jordansmall/spindrift/commit/228dcebe12e0cf6350d829424016bcc0386155ff))
* **runner:** add RunNixBuild for a fresh in-session rebuild ([739390b](https://github.com/jordansmall/spindrift/commit/739390b40ef340495232a11a8162af951517586b))
* **runner:** list running sandboxes by name ([0618cd8](https://github.com/jordansmall/spindrift/commit/0618cd88563df80bc9ad2fdef16fdd30bbda17ea))
* **schema:** add choices field to enumerable flag knobs ([c7ad6db](https://github.com/jordansmall/spindrift/commit/c7ad6db76ee82ef44e18ab615f2b9e58c7ab51dc))
* **settle:** abandon settle when terminated mid-flight ([17b2048](https://github.com/jordansmall/spindrift/commit/17b2048c4001352e3813733999e558584a4ec554))
* **settle:** add one-shot ResearchSettle ([d95cd47](https://github.com/jordansmall/spindrift/commit/d95cd47dd85f2f85fb84bc5131607c9941d7aed5))
* **settle:** replace gate boolean pair with GateResult enum ([a3320a3](https://github.com/jordansmall/spindrift/commit/a3320a3d7c928fc1ddce866bc73f1c16a1c92a09))
* **settle:** replace landing boolean pair with LandingResult enum ([c589a1f](https://github.com/jordansmall/spindrift/commit/c589a1fcbb58570dd53d32572ad6542e1c6de8ae))
* **terminate:** add termination registry package ([7931bca](https://github.com/jordansmall/spindrift/commit/7931bca895e072fa29f14dd59b320ea80edcc58a))
* **waves:** add BlockerStatus non-mutating readiness check ([afc34de](https://github.com/jordansmall/spindrift/commit/afc34de0e49e75dedaf84766770b6a0e86649b9d))
* **waves:** let research ignore blocker edges ([54a5804](https://github.com/jordansmall/spindrift/commit/54a58046f7ed0d0c5b3966c8d8279a38edb36632))
* **waves:** resizable concurrency Limiter for the engine ([0ac132b](https://github.com/jordansmall/spindrift/commit/0ac132bb63dffc4e5103fe26de8d9f18c7659a0d))
* **waves:** skip Failed transition on a terminated issue ([4fca503](https://github.com/jordansmall/spindrift/commit/4fca5036c8bc2819bbd55568e8c654051f9d0f3b))
* **waves:** thread blocker source through edges and marker ([e3478a2](https://github.com/jordansmall/spindrift/commit/e3478a2dd7d1d11f01a33238d3cc9489bb6f39f6))


### Bug Fixes

* address reviewer findings on research subcommand discoverability ([11ca6eb](https://github.com/jordansmall/spindrift/commit/11ca6eb6142de8d5afc6e1e7da656d9a0532be61))
* **checks:** guard against a stale Unreleased heading ([6e12092](https://github.com/jordansmall/spindrift/commit/6e12092f4ee4c3a3bba8e26888ed084cc588002a))
* **checks:** normalize [Unreleased] heading match ([b6195d1](https://github.com/jordansmall/spindrift/commit/b6195d1c236c579bd4f0a38f9a04ce9118194b2f)), closes [#666](https://github.com/jordansmall/spindrift/issues/666)
* **claude:** classify mid-stream 5xx errors as transient ([198dceb](https://github.com/jordansmall/spindrift/commit/198dcebecc4f846873d9d26239b2b05eed0fcb9f)), closes [#815](https://github.com/jordansmall/spindrift/issues/815)
* **console:** budget column labels, not just their rows ([9053251](https://github.com/jordansmall/spindrift/commit/90532518af38888e4867c18d01a55dc793f1651e))
* **console:** budget the trailing refresh-error line ([17cc4b8](https://github.com/jordansmall/spindrift/commit/17cc4b8d034de9db18b182935d97085c6d02c8f8))
* **console:** clip joined rows to terminal width ([3f794e0](https://github.com/jordansmall/spindrift/commit/3f794e0e6e1386800ac17848e0c07d962ceb1f83))
* **console:** dedup active picks, gate rebuild on stale ([fb3025a](https://github.com/jordansmall/spindrift/commit/fb3025a992da46d8bef386c8468591cde80de3ff))
* **console:** fix review findings in the keypress wiring ([c263258](https://github.com/jordansmall/spindrift/commit/c263258bdc5a4dd5fd6b028ff56ac8cd8cc2e2b8))
* **console:** land a dissolved promotion row on Queue too ([64d871e](https://github.com/jordansmall/spindrift/commit/64d871e575739753504a9942e58818abe8b93f13))
* **console:** make freshness checker tests git-free ([269ffaa](https://github.com/jordansmall/spindrift/commit/269ffaaaef00afb7bd092e2361a50d1451a5d97c))
* **console:** match rebuild-clears-stale test to real Fresh ([47ccdfe](https://github.com/jordansmall/spindrift/commit/47ccdfed9357978c3ca1d55dbdff303bc59dafd3))
* **console:** page by rendered rows, not the raw budget ([c4622e6](https://github.com/jordansmall/spindrift/commit/c4622e65d56aed77d3b8c54dc9a3b1043090c9e1))
* **console:** probe pid liveness in DogfoodNotice ([41792c9](https://github.com/jordansmall/spindrift/commit/41792c91e5a7620594f15b97908e71321312be9a)), closes [#690](https://github.com/jordansmall/spindrift/issues/690)
* **console:** queue setState targets newest matching pick ([1b574e4](https://github.com/jordansmall/spindrift/commit/1b574e4d215824e9697eb77cf37c815db05a0aeb))
* **console:** queueSettler skips a terminated pick's row ([ca3dbd6](https://github.com/jordansmall/spindrift/commit/ca3dbd6b101950ac9a0221d3afd8f266d78a43cb))
* **console:** re-check freshness before parking a stale drain ([1835356](https://github.com/jordansmall/spindrift/commit/1835356c81fa3c94b51087d5e5078a8673694418))
* **console:** re-evaluate held picks on the background poll ([21aed7d](https://github.com/jordansmall/spindrift/commit/21aed7d3cdab5997b9bab9176a3fec98efbb469c))
* **console:** reject picks on already-terminal issues ([ef3adbf](https://github.com/jordansmall/spindrift/commit/ef3adbf490955d52cb4daacdf36cfb44c21bc165)), closes [#707](https://github.com/jordansmall/spindrift/issues/707)
* **console:** rename operator-report pr= label to landing= ([1e9437f](https://github.com/jordansmall/spindrift/commit/1e9437f29b097b066ffc6ae48e2a4046de52adba)), closes [#655](https://github.com/jordansmall/spindrift/issues/655)
* **console:** restore fmt import after rebase onto [#786](https://github.com/jordansmall/spindrift/issues/786) ([da47c85](https://github.com/jordansmall/spindrift/commit/da47c85756d44eec22c96f9e63c7de2f2b521006))
* **console:** route Box failures to a terminal queue state ([0753e98](https://github.com/jordansmall/spindrift/commit/0753e98bca86e30c044ff11a399f979673040c52)), closes [#705](https://github.com/jordansmall/spindrift/issues/705)
* **console:** sanitize rendered transcript output ([6cba85f](https://github.com/jordansmall/spindrift/commit/6cba85f9f8afad55816395edf5a0d216e6e9d948)), closes [#721](https://github.com/jordansmall/spindrift/issues/721)
* **console:** scope terminate to live picks only ([46200e2](https://github.com/jordansmall/spindrift/commit/46200e212b249bf9c1aa0a7f3f26750e4d1ec5a2))
* **console:** split body budget on stacked layout ([7d27b32](https://github.com/jordansmall/spindrift/commit/7d27b326dd10048e06af72fdb41dbdc05378fe40))
* **console:** stop racy fr.RunCalls poll in held-pick test ([694a1f4](https://github.com/jordansmall/spindrift/commit/694a1f4bfa6aff239ec0cc753fcfe6e05ddf19ab))
* **console:** stop stale BlockedBy and unpick/claim race ([f97030a](https://github.com/jordansmall/spindrift/commit/f97030ace57db4ec263ba0e03e3aa275195b4152))
* **console:** sync rendered picks with the live queue ([337bb0a](https://github.com/jordansmall/spindrift/commit/337bb0a315750e5289ee94ed2da0621383f3a69d))
* **console:** terminate clears Complete label too ([3e74106](https://github.com/jordansmall/spindrift/commit/3e74106f5bee803cab2598f9d2b167e9a471be63))
* **console:** truncate transcript results on rune boundary ([712a2ae](https://github.com/jordansmall/spindrift/commit/712a2aea9d058c5fd5d0f8cfae01596b05ab0e5a)), closes [#717](https://github.com/jordansmall/spindrift/issues/717)
* **console:** tryMarkClaiming scans back-to-front for duplicate numbers ([f210a48](https://github.com/jordansmall/spindrift/commit/f210a4860f24a43de0d3513c6ad1742e70e16a50))
* **docs:** match reference.md label snippet to doctor defaults ([2745140](https://github.com/jordansmall/spindrift/commit/2745140086c4cec96a9bece0a4a325b8cdadc5fd))
* **dogfood:** make memory preflight parallelism-aware ([5f215a9](https://github.com/jordansmall/spindrift/commit/5f215a9f57504d41be1c7645baaeb56ee2198445)), closes [#712](https://github.com/jordansmall/spindrift/issues/712)
* **driver-exec:** address reviewer non-blocking findings ([8c1867d](https://github.com/jordansmall/spindrift/commit/8c1867d4fa224472df0731c0c7988ab88d9c8903))
* **drivers:** keep DRIVER_BIN/FLAGS_COMMON byte-identical ([7f1e9d7](https://github.com/jordansmall/spindrift/commit/7f1e9d742a9a80c17a149d15ecf1365d36721d96))
* **drivers:** keep escaping intact in rendered preamble ([42d171a](https://github.com/jordansmall/spindrift/commit/42d171a6f38f583226291269165da48032fa9e3f))
* **drivers:** restore byte-identical baked preamble ([30aa32f](https://github.com/jordansmall/spindrift/commit/30aa32fe440b9a42a92a9000ee5539ef3892e1c3))
* **forge/exec:** assert InProgress before CompleteVerdict edit ([998915d](https://github.com/jordansmall/spindrift/commit/998915d608e5d5c171cdae18b96680e8a238064a)), closes [#701](https://github.com/jordansmall/spindrift/issues/701)
* **forge:** detect staleness via compare API, not mergeStateStatus ([9c132af](https://github.com/jordansmall/spindrift/commit/9c132aff1241041766df67d365cc65c513d24056))
* **launcher:** dedupe native dependency numbers ([07309bc](https://github.com/jordansmall/spindrift/commit/07309bcb0adbbd426c5992e6a65a1483d9da6bbe)), closes [#632](https://github.com/jordansmall/spindrift/issues/632)
* **launcher:** log native dep warnings to stderr ([8c5a401](https://github.com/jordansmall/spindrift/commit/8c5a401630bb74423f9cdd01b6a88d22a3d966a0)), closes [#631](https://github.com/jordansmall/spindrift/issues/631)
* **launcher:** point schema-default tests at schemaFlags ([82b3cc5](https://github.com/jordansmall/spindrift/commit/82b3cc54dec03218deeca7d8da8c5ff46408cfee))
* **launcher:** read defaults from schemaFlags, drop table ([f8d9e9b](https://github.com/jordansmall/spindrift/commit/f8d9e9b9ef19b1b9b7934f16e61dade3ec355e07)), closes [#670](https://github.com/jordansmall/spindrift/issues/670)
* **launcher:** retain sources in continuous-dispatch discover ([8012f9d](https://github.com/jordansmall/spindrift/commit/8012f9df5c9bbd8f12ca65419b49b23edca71769))
* **launcher:** surface gh stderr in native deps error ([5fdb28b](https://github.com/jordansmall/spindrift/commit/5fdb28bb6a7654dc8f07e1e298c3ab46197b9d48)), closes [#633](https://github.com/jordansmall/spindrift/issues/633)
* **launcher:** update console bootstrap call for new signature ([cbaadcf](https://github.com/jordansmall/spindrift/commit/cbaadcf646a71cf35fe2bdfde60c8b6cf644dcc7))
* **local:** guard CompleteVerdict against unconfigured verdicts ([6dfa58c](https://github.com/jordansmall/spindrift/commit/6dfa58cf2a7b0de7afe8bf8d8769cd3fb8681fee))
* **nix:** copy docs/ into launcher-go-test sandbox ([31ffd25](https://github.com/jordansmall/spindrift/commit/31ffd253528aa3c43b6b16a6cf5f3d45a180d1f1))
* **nix:** mirror docs/ into launcherBin's buildGoModule src ([6888ff1](https://github.com/jordansmall/spindrift/commit/6888ff14968dcd059eeb553792c99ebd066222e5))
* **nix:** split driver-exec's vendorHash from launcherBin's ([34961a1](https://github.com/jordansmall/spindrift/commit/34961a1acab4ed7240663e72cc45df072cb6bb60))
* **runner:** Kill treats a missing sandbox as success ([482d45d](https://github.com/jordansmall/spindrift/commit/482d45dc27ea1c060ec4f608ef3baa46bb6e4bc4))
* **sandbox:** raise MEMORY_LIMIT default to 5g ([013cf16](https://github.com/jordansmall/spindrift/commit/013cf16abf8ac8406559f287ef54fc1545c168ef))
* **settle/adopt:** guard verifyMerged against nil pr ([f4e4c1b](https://github.com/jordansmall/spindrift/commit/f4e4c1b2f2727f7e2f546b764b28de5f329edf5d)), closes [#697](https://github.com/jordansmall/spindrift/issues/697)
* **settle/research:** suppress success log on CompleteVerdict error ([65e03c0](https://github.com/jordansmall/spindrift/commit/65e03c08bdf7cd8261fd6fab36604283d0d42dd1)), closes [#699](https://github.com/jordansmall/spindrift/issues/699)
* **settle:** fail issue when force-pushed head never greens ([07d5030](https://github.com/jordansmall/spindrift/commit/07d50303ca5b7f272215b06ee7b3b3e27a071663)), closes [#758](https://github.com/jordansmall/spindrift/issues/758)
* **settle:** hold agent-complete until landing settles ([c342668](https://github.com/jordansmall/spindrift/commit/c342668babac517a3d6a7e8c97ed9dac066b72f8)), closes [#757](https://github.com/jordansmall/spindrift/issues/757)
* **settle:** keep gate.go's operator report on pr= label ([9c31b65](https://github.com/jordansmall/spindrift/commit/9c31b651dd454af5b519aa1411df18ef9f18e2b8))
* **settle:** rebase a stale-but-green PR before merging ([fda1a20](https://github.com/jordansmall/spindrift/commit/fda1a20624b37429d1f07aea1ed55928c08db536))
* **settle:** update stale selfHeal doc, comment wording ([7a298de](https://github.com/jordansmall/spindrift/commit/7a298de938c3234f8b7c5e5093400dbcf8d5e425))
* **skills:** bake skills as &lt;name&gt;/SKILL.md dirs so Claude Code loads them ([92e1739](https://github.com/jordansmall/spindrift/commit/92e17390b9d049b5dd18b8d9b37ee8ddbbe5d465))
* **tests:** centralize the DRIVER_SKILLS_DIR test override ([5d7f17c](https://github.com/jordansmall/spindrift/commit/5d7f17cd34ca2ecb8ad8f3f386094e055b7ef136))
* **usage:** preserve totals on BreakdownByRole I/O error ([cd322b8](https://github.com/jordansmall/spindrift/commit/cd322b85b66b72c1685d83d790ab13bb5914f745)), closes [#674](https://github.com/jordansmall/spindrift/issues/674)
* **waves:** surface it.TouchesOf fetch errors ([1644656](https://github.com/jordansmall/spindrift/commit/16446561b297b2f042870b5740f3e0081c40870b))
* **workflows:** read agent App IDs from vars, not secrets ([39ffdbb](https://github.com/jordansmall/spindrift/commit/39ffdbb2e29cd990aadc19941c82942a7ddf49a4))


### Performance Improvements

* **console:** cache drill-in line-split across re-renders ([86f2339](https://github.com/jordansmall/spindrift/commit/86f23396356cbca075770b3bdc8f0aa04e136bfe)), closes [#722](https://github.com/jordansmall/spindrift/issues/722)
* **console:** window transcript render to viewport height ([c5dfd17](https://github.com/jordansmall/spindrift/commit/c5dfd175f250373835fd7899a84eb22fabb92bef))


### Documentation

* add a Console section to the README ([4333d76](https://github.com/jordansmall/spindrift/commit/4333d761ee7acb65a7666b0ace627164e85b6713))
* **adr:** add ADR 0022 research is a dispatch kind ([b3a9266](https://github.com/jordansmall/spindrift/commit/b3a9266e21050f23972360617681947a7c3a798f))
* **adr:** add ADRs 0020 and 0021 ([8324dbe](https://github.com/jordansmall/spindrift/commit/8324dbedde484d97197d71de226a311b856f41b4))
* **adr:** add ADRs 0023 and 0024 ([6974bda](https://github.com/jordansmall/spindrift/commit/6974bdabd0dbbd318418d3235937c58bedb365ca))
* **adr:** amend 0009 and 0010 for opencode's unshipped status ([04e5b32](https://github.com/jordansmall/spindrift/commit/04e5b3245b911c53b34fd66560981a3db42c9414))
* **adr:** carve out never-green re-wait from ADR 0012 ([9fe36d0](https://github.com/jordansmall/spindrift/commit/9fe36d056d9762f6dcc24818a956c6edefcdf65a))
* **adr:** record the stale-base preflight decision ([6e8370b](https://github.com/jordansmall/spindrift/commit/6e8370ba01786b04cc65cd17baa53365e5b23144))
* **auth:** document GitHub App token auth for dispatch & recover ([49b542a](https://github.com/jordansmall/spindrift/commit/49b542ac53187cd594cfe16720cb1a0864c7a784)), closes [#1025](https://github.com/jordansmall/spindrift/issues/1025)
* **changelog:** drop orphaned Unreleased block ([fe89f90](https://github.com/jordansmall/spindrift/commit/fe89f90979c5e75ae49cfb9c6db82ab3a8c13d55))
* **checks:** clarify changelog regex-escape comment ([da37edf](https://github.com/jordansmall/spindrift/commit/da37edf446aac49348a599a1a3037379777b689c)), closes [#667](https://github.com/jordansmall/spindrift/issues/667)
* **claude:** grep build output from disk, never stream it ([9e1de25](https://github.com/jordansmall/spindrift/commit/9e1de2587d759c61fbcc650dea63d2c13e217a23))
* **claude:** point issue-filing at /to-tickets ([fcb8d4d](https://github.com/jordansmall/spindrift/commit/fcb8d4d9dac076fd8d19a479cbd687fee84f7811))
* **console:** correct Width/Height clamp comment ([7339163](https://github.com/jordansmall/spindrift/commit/7339163f00efa3bb491fd6ee12802f48d2b7e04c))
* **console:** document drill-in as one-shot, not live-tail ([4456cbe](https://github.com/jordansmall/spindrift/commit/4456cbe9a5c543b183cfaff06b0d985ce281f6da)), closes [#719](https://github.com/jordansmall/spindrift/issues/719)
* **console:** explain the Config zero-value claim coupling ([0e9426e](https://github.com/jordansmall/spindrift/commit/0e9426e6ef82b252d88d7b71dd1c2522cb482b77)), closes [#706](https://github.com/jordansmall/spindrift/issues/706)
* **console:** fix Run doc on background-poll nil-launch behavior ([81fceb9](https://github.com/jordansmall/spindrift/commit/81fceb91fcdd55191d228c272f24113c70456de4))
* **console:** fix stale run-loop comment, list ctrl+c in help ([ed5b9ef](https://github.com/jordansmall/spindrift/commit/ed5b9efed115e46262382a5beca0c13c4f98a36f))
* **console:** fix two more stale j/k comments ([93bd0ef](https://github.com/jordansmall/spindrift/commit/93bd0ef6d731b71114ad109bdd2cd4777ef382e0))
* **console:** list new keys in the help overlay ([d4f25f7](https://github.com/jordansmall/spindrift/commit/d4f25f725b6a64ea2ebc492eb086eb7b322b0594))
* **console:** note raw transcript view control-seq risk ([8fade13](https://github.com/jordansmall/spindrift/commit/8fade13c559d561bf7e3889a81da9f484e74a457))
* **console:** record multi-region layout in ADR 0025 ([f5d87fc](https://github.com/jordansmall/spindrift/commit/f5d87fc28c72291cf9d2dab5ba981581b58c4fd3))
* **context:** add Console era terms ([4d6f910](https://github.com/jordansmall/spindrift/commit/4d6f910425c9e6775b8a0e9ecd24242083bcf876))
* **context:** add launcher-input era terms ([01dc604](https://github.com/jordansmall/spindrift/commit/01dc60475ddf5d6b02c907f360f0ea81cd4b7be1))
* **context:** add research dispatch kind terms ([044d683](https://github.com/jordansmall/spindrift/commit/044d6833d454305a8ad1bdd61031f1b483799c70))
* **context:** clarify Provider availability in definition ([84821b9](https://github.com/jordansmall/spindrift/commit/84821b95f0bed9542a22bd920e53943d97fd4327))
* **context:** document native-first blocker precedence ([173756a](https://github.com/jordansmall/spindrift/commit/173756ae2e622508a8b2b02b52ef1b56f274477b))
* **context:** drop driver-exec's lands-with note ([118fdae](https://github.com/jordansmall/spindrift/commit/118fdae95cac92a2363148fa92ccef315c10d162))
* **context:** mark opencode Driver as design target, not shipped ([c16f060](https://github.com/jordansmall/spindrift/commit/c16f0607a9b785c51ad7f28f8cf1095a10b7ea48))
* **contributing:** drop defaults_gen.go references ([7e2a271](https://github.com/jordansmall/spindrift/commit/7e2a271adaff01b4a63793d95ece46cccefbf20b))
* describe the settled-landing agent-complete swap ([cae2fa3](https://github.com/jordansmall/spindrift/commit/cae2fa31c17762d4580e928827160a65e38fb81d))
* doctor now manages research labels too ([160c4fb](https://github.com/jordansmall/spindrift/commit/160c4fb6248946f1cb66884a76da0747aba1fb7f))
* document ADR 0020's flag/settings precedence ([ee0844d](https://github.com/jordansmall/spindrift/commit/ee0844dab2bef895ba3b2aafb4e95ddd379a2dff))
* document blocker source annotation ([5f0b83b](https://github.com/jordansmall/spindrift/commit/5f0b83b1f280499c6cb4b5c2eec245c733260006))
* document Console pick and unpick ([44ebb1c](https://github.com/jordansmall/spindrift/commit/44ebb1c9d94e1eb12712a33d1f31e867cfc0a20b))
* document the research subcommand and add its bats coverage ([a530212](https://github.com/jordansmall/spindrift/commit/a530212692bf072750c8a8d0b59cd446bbabf4da))
* **driver:** reflect the claude subpackage split ([2687a0f](https://github.com/jordansmall/spindrift/commit/2687a0faed63fe6ffceb3c151c44026e054d3d98))
* **equivalence:** mention code-review in parity comment ([2d31224](https://github.com/jordansmall/spindrift/commit/2d31224f273269a79579ce09d900633a99b834ae))
* fix devShellName option path in README ([e738882](https://github.com/jordansmall/spindrift/commit/e7388828266b213848ff07fafefe1e4ef8687af9)), closes [#615](https://github.com/jordansmall/spindrift/issues/615)
* fix stale run/build wording from reviewer pass ([d76e3c7](https://github.com/jordansmall/spindrift/commit/d76e3c784429b300154a6728b0d2be4ab00623bd))
* **forge:** document TouchesOf's github-body-fetch consequence ([45a8014](https://github.com/jordansmall/spindrift/commit/45a80149cfbc84e8150c5d2825630b6174357737))
* **jira:** note research verdicts stay label-only ([de1faf2](https://github.com/jordansmall/spindrift/commit/de1faf2e87e95d294fe7dd2e86d3e9bb590dea21))
* list code-review among baked dogfood skills ([5993da5](https://github.com/jordansmall/spindrift/commit/5993da5cb99669ee3f82a25cd09cb58017106dc3))
* mention defaults_gen.go in the regen artifact list ([c9266ae](https://github.com/jordansmall/spindrift/commit/c9266ae3da5f4af1190899a123f241935e2d9d9a))
* **prompt:** emit landing= in the outcome contract ([4e7e788](https://github.com/jordansmall/spindrift/commit/4e7e788054917e790a4725beeac42bf8237b9822))
* **readme:** clarify local blocker resolution wording ([606b2f3](https://github.com/jordansmall/spindrift/commit/606b2f3b7a23d366f9f9479f5df273f42df60939)), closes [#634](https://github.com/jordansmall/spindrift/issues/634)
* **readme:** document console drill-in and raw toggle ([61c7ef0](https://github.com/jordansmall/spindrift/commit/61c7ef06358d91cf47b2ad183fd1eb03e50a8ef5))
* **readme:** document held picks in Console section ([5739b0d](https://github.com/jordansmall/spindrift/commit/5739b0d5712700b871d29a65bab49a510f89f787))
* **readme:** document pick-all-ready and freshness ([459af1e](https://github.com/jordansmall/spindrift/commit/459af1ea535d866b0c4b87477df957eb778e58be))
* **readme:** document quit and orphan recovery ([bdcd60f](https://github.com/jordansmall/spindrift/commit/bdcd60fe51579435a5541c7e6ac987b7aa965295))
* **readme:** document stale banner and rebuild key ([5998177](https://github.com/jordansmall/spindrift/commit/59981777fc4c814e9a9c0a47593157363b568395))
* **readme:** document terminate in console section ([0fc34a8](https://github.com/jordansmall/spindrift/commit/0fc34a85a8961ceb3807b511f32cd9a54ac8a1e9))
* **readme:** document the live parallelism cap keybinding ([018fdc7](https://github.com/jordansmall/spindrift/commit/018fdc70ddc84d3cfc4976edd894ae09a0f68c5c))
* **readme:** note enumerable flag-value completion ([bf8d816](https://github.com/jordansmall/spindrift/commit/bf8d8163f17ada49b352d55e5e06276119f76e8f))
* record Quickstart design in ADR 0027 and glossary ([6dc8abd](https://github.com/jordansmall/spindrift/commit/6dc8abd549f2cb8d780713968a4fe41226cbdddf))
* record run/build alias removal as done ([563724f](https://github.com/jordansmall/spindrift/commit/563724f95d1d6b7bd3db8d32fe0698cc7a445935))
* **reference:** clarify system option scope and settability ([5a5993f](https://github.com/jordansmall/spindrift/commit/5a5993ffebe9ec35459e6d497921251ac2e9f179)), closes [#660](https://github.com/jordansmall/spindrift/issues/660)
* **reference:** describe the Conditional fragment registry ([0bd9a93](https://github.com/jordansmall/spindrift/commit/0bd9a93f0f1a8a6bb7f81661af28370e647e4f06))
* **reference:** document native-first GitHub blocker resolution ([6d53763](https://github.com/jordansmall/spindrift/commit/6d53763fc9d7ad213fa92acb04c13bfe983d5959))
* **reference:** document registry validation and rendering ([3620c65](https://github.com/jordansmall/spindrift/commit/3620c6598f9be419c735ffdeaadb242e9bc95f85))
* **reference:** fix stale 4g example in sandbox config ([aca3f4e](https://github.com/jordansmall/spindrift/commit/aca3f4e352759228b9cee4a6f644721a81a685a5))
* **reference:** fix stale DRIVER_SKILLS_DIR description ([1a371b9](https://github.com/jordansmall/spindrift/commit/1a371b9005a2108b767a33db41204e329b6112ba))
* **reference:** mark mkHarness-only flake option knobs ([565d6a7](https://github.com/jordansmall/spindrift/commit/565d6a7b5b02dc10e953ac5aead3eab62f02beff)), closes [#612](https://github.com/jordansmall/spindrift/issues/612)
* **research:** document label family, token, dogfood path ([72f61aa](https://github.com/jordansmall/spindrift/commit/72f61aaea9f672693daa4ed870de4b9eca2b28f0))
* **research:** document research-scope GitHub App auth ([56dd806](https://github.com/jordansmall/spindrift/commit/56dd80646c1abd3ade9d9274bfe0433684089177)), closes [#1026](https://github.com/jordansmall/spindrift/issues/1026)
* update outcome grammar to landing= across README/CONTEXT/reference ([bab08fb](https://github.com/jordansmall/spindrift/commit/bab08fba5de2b38e7efc7bdc67b1923ae5ae561d))
* **waves:** document Sources design invariant ([b2ccad5](https://github.com/jordansmall/spindrift/commit/b2ccad5425a99d0a7e3d4800f81208aa34c3dca3)), closes [#663](https://github.com/jordansmall/spindrift/issues/663)
* **waves:** document TouchesOf fetch-failure fallback ([8a7669d](https://github.com/jordansmall/spindrift/commit/8a7669dff246f19f0766faf9bb469baf5f24f789))


### Code Refactoring

* **agent:** replace six conditional prompt blocks with one loop ([85e361f](https://github.com/jordansmall/spindrift/commit/85e361f3836bf42b69bc96f5349019ca6eeb3132))
* **forge:** absorb PRForge assertion into resolver ([c62a828](https://github.com/jordansmall/spindrift/commit/c62a828ba236f91229460eef130edabac53144ad)), closes [#682](https://github.com/jordansmall/spindrift/issues/682)
* **forge:** encapsulate blocker regex grammar ([774b530](https://github.com/jordansmall/spindrift/commit/774b530ea81cd60f8949b9bb11ec36186e701337)), closes [#680](https://github.com/jordansmall/spindrift/issues/680)
* **forge:** move gitplumbing exports to subpackage ([b1d0489](https://github.com/jordansmall/spindrift/commit/b1d048940b8bc9be88fbdcb51bbd1e56520d2d60)), closes [#684](https://github.com/jordansmall/spindrift/issues/684)
* **forge:** split adapters into per-adapter subpackages ([bca1a6c](https://github.com/jordansmall/spindrift/commit/bca1a6c9d3a41ca70e7b06db9774b2ac4df3e4c6))
* **launcher:** follow outcome.PR rename to Landing ([f8d6a40](https://github.com/jordansmall/spindrift/commit/f8d6a40cd58ef72a20d5fea11eec2f0020ae4b48))
* **launcher:** retire redundant OpenPRForIssue guard ([9013c99](https://github.com/jordansmall/spindrift/commit/9013c999c0b608f00a0e4807f1be3bf8a23bb246)), closes [#683](https://github.com/jordansmall/spindrift/issues/683)
* **prompts:** drop redundant fix-side vanished-marker pin ([29080e7](https://github.com/jordansmall/spindrift/commit/29080e7dd20c074b3ebd97369287e824c13e6803)), closes [#725](https://github.com/jordansmall/spindrift/issues/725)
* **settle:** colocate the ready path in one file ([8efceb6](https://github.com/jordansmall/spindrift/commit/8efceb6a821c11aa947d28ebe610f7137e2ddb08))
* **settle:** unexport the gate and landing result enums ([f205fb3](https://github.com/jordansmall/spindrift/commit/f205fb344c46864af1c284c04c4643cb52234838))
* **usage:** unexport LastInLog and BreakdownByRole ([92f4b25](https://github.com/jordansmall/spindrift/commit/92f4b2591e53730890594172c5ed158c6c0e3c74)), closes [#676](https://github.com/jordansmall/spindrift/issues/676)


### Tests

* **bats:** drop stale no-warning checks for missing dirs ([44e73ed](https://github.com/jordansmall/spindrift/commit/44e73ed7948470776ba59a3f850c373084d2f921))
* **bats:** retarget driver invocation tests for driver-exec ([e251c9d](https://github.com/jordansmall/spindrift/commit/e251c9d1a6d4358b7af02923a947a4c6003cd182))
* **bats:** update outcome fixtures to landing= token ([329a093](https://github.com/jordansmall/spindrift/commit/329a093cca73d8aaa6b4e0be7d5c1817b4dfc79b))
* **claude:** add mirror assertion for Class/Reason enums ([a85c44f](https://github.com/jordansmall/spindrift/commit/a85c44ff1ef9bb1c0ba37307e0d929b4ae316e6c))
* **console:** benchmark drill-in keystroke re-render ([f4a932c](https://github.com/jordansmall/spindrift/commit/f4a932cbe226cee318ebfa1d3802cbc98b1af4f3))
* **console:** cover held-pick hold, launch, and failure ([96c7675](https://github.com/jordansmall/spindrift/commit/96c7675eb1a06e687fd54053e7dd77dc41b90701))
* **console:** cover launch-less session header rendering ([6ec7088](https://github.com/jordansmall/spindrift/commit/6ec70884a4dcde24fdc804f905fd998b34a69fa1))
* **console:** cover parallel slots, refresh, and heartbeat rows ([58b8ec0](https://github.com/jordansmall/spindrift/commit/58b8ec0ac4614d899e7e1cb50287eacd36a744c3))
* **console:** cover the page jump and position indicator ([a37b262](https://github.com/jordansmall/spindrift/commit/a37b262f6050c7b1a51a8dae287b56e9e8928c37))
* **console:** keep existing comment style ([a6ca0ad](https://github.com/jordansmall/spindrift/commit/a6ca0ad69816f066ba269dead288176b82e3cef6))
* **console:** pin down queued-but-unlaunched stays Dispatchable ([62ff9c4](https://github.com/jordansmall/spindrift/commit/62ff9c4e4d1e585366f9c164eb9c906dcec02029))
* **console:** pin Label/InProgressLabel claim coupling ([3d3a6f7](https://github.com/jordansmall/spindrift/commit/3d3a6f778c73e8dcface30cb981a02f96f7e0e02)), closes [#706](https://github.com/jordansmall/spindrift/issues/706)
* **console:** prove one Dispatch launches end to end ([7dd4153](https://github.com/jordansmall/spindrift/commit/7dd4153b23f8c6ef99a5a3c5ede67a7c2381ca59))
* **drivers:** prove no behaviour change for skills dir ([216c26d](https://github.com/jordansmall/spindrift/commit/216c26d7650d6ac23727127a2314810f1a926a17))
* **entrypoint-prompt-fragments:** fix stale coverage comment ([eb3da68](https://github.com/jordansmall/spindrift/commit/eb3da6847a474c12e1667476d40be67dc55415e7)), closes [#688](https://github.com/jordansmall/spindrift/issues/688)
* **entrypoint:** collapse fragment tests to mechanism + fixture row ([5a739bd](https://github.com/jordansmall/spindrift/commit/5a739bdc2dc8c2dfe3385662c16e4fa05934634b))
* **entrypoint:** cover CODE_REVIEW_STEP gate on/off ([1f5200e](https://github.com/jordansmall/spindrift/commit/1f5200e5a7b7368c9c19c18a222a47fc3964acd0))
* **entrypoint:** cover TDD_BAKED and COMMIT_BAKED gates ([cbf3a98](https://github.com/jordansmall/spindrift/commit/cbf3a98ea42971075d076c25649beef3bf14e90a))
* **entrypoint:** cover the research dispatch kind path ([162a1b1](https://github.com/jordansmall/spindrift/commit/162a1b1f30d6b3d95969048f9ad50cbbe41e819a))
* **equivalence:** repoint module-parity checks at spindrift ([d20c5ea](https://github.com/jordansmall/spindrift/commit/d20c5eaed76098630e8c18153466b8c6b436e968))
* **fakes:** emit landing= from the fake claude driver ([1c3cbe3](https://github.com/jordansmall/spindrift/commit/1c3cbe3bcff50fc0dc06478413d8a05d46408aa5))
* **forge:** cover DepSource unknown fallback and Ref ([091ce61](https://github.com/jordansmall/spindrift/commit/091ce61cc0108d84efc994a6222724806c8e94bb)), closes [#661](https://github.com/jordansmall/spindrift/issues/661)
* **forge:** pin research InProgress/Failed label distinctness ([d3e2ba1](https://github.com/jordansmall/spindrift/commit/d3e2ba1507e713d7bc0da01e0255bd0a2caba452))
* **jira:** cover research verdict transitions and retry ([27d884b](https://github.com/jordansmall/spindrift/commit/27d884b0148b0ed934e48af0a5a1795ed61ff090))
* **launcher:** assert triage label keys covered once ([d567e55](https://github.com/jordansmall/spindrift/commit/d567e552ef420d0f8fcc9a30d77bb9d70296b339)), closes [#657](https://github.com/jordansmall/spindrift/issues/657)
* **launcher:** cover console subcommand routing ([ed243ee](https://github.com/jordansmall/spindrift/commit/ed243eeeedb7df613a30b3ff87ba75a43d0715c5)), closes [#694](https://github.com/jordansmall/spindrift/issues/694)
* **launcher:** cover int schema helpers directly ([a463411](https://github.com/jordansmall/spindrift/commit/a463411051b74ebc08fe70343511d8d06c012836)), closes [#672](https://github.com/jordansmall/spindrift/issues/672)
* **launcher:** cover kind-aware label wiring end to end ([6e91b56](https://github.com/jordansmall/spindrift/commit/6e91b560b823ba22e300fb4f50ff33a9be0909d5))
* **launcher:** prove mainRun's ambient-env warning fires ([b23610a](https://github.com/jordansmall/spindrift/commit/b23610ae4a214f91f2cee24181a8bfdb4bf424f3))
* **prompts:** add fragment gate name parity check ([4237f39](https://github.com/jordansmall/spindrift/commit/4237f39aeeea3d157e2b0526c020584be56cef72))
* **prompts:** add nix check for git-add guidance ([6d93393](https://github.com/jordansmall/spindrift/commit/6d93393f44c0907ef451cb5a80746bee62f5c7b2))
* **prompts:** cover fix-prompt vanished-marker in bats ([c6ec61f](https://github.com/jordansmall/spindrift/commit/c6ec61fdda6029a0e9cbcc386ac31902baa34742)), closes [#726](https://github.com/jordansmall/spindrift/issues/726)
* **prompts:** cover git-add-before-nix-build guidance ([fc5269f](https://github.com/jordansmall/spindrift/commit/fc5269f14912377f75bf240def9ca0765ada4817))
* **prompts:** pin landing= in research outcome contract ([6af7bc2](https://github.com/jordansmall/spindrift/commit/6af7bc2b6b74cfcf0b8f3c00a68812d42c5c277d))
* **prompts:** pin landing= token in outcome contract ([34e24e1](https://github.com/jordansmall/spindrift/commit/34e24e141f073ec19a045f08953f307bd5b2f2f2))


### Build System

* **mkharness:** bake driver-exec, drop heartbeat-filter ([16d6a83](https://github.com/jordansmall/spindrift/commit/16d6a830b72581e6de093b21e8b43882544066a9))
* **nix:** add console dependency isolation check ([924ce75](https://github.com/jordansmall/spindrift/commit/924ce755fad9ec30c6069f3ac062943ee7f55983)), closes [#695](https://github.com/jordansmall/spindrift/issues/695)
* **nix:** pin CODE_REVIEW_BAKED in fragment-gate-parity ([eac026e](https://github.com/jordansmall/spindrift/commit/eac026e2638ebee42d4adb0a763636c891ff72ee))
* **regen:** stop generating defaults_gen.go ([1d7d095](https://github.com/jordansmall/spindrift/commit/1d7d09571e2127729bbba6854ec97fb679faa68c))


### Continuous Integration

* **agent-setup:** preflight a rate-limit smoke test before the build ([09acc2d](https://github.com/jordansmall/spindrift/commit/09acc2d77778c35078709a2a27941fe7248cd1a6))
* **agent-setup:** surface rate-limit result in the job summary ([38d7d99](https://github.com/jordansmall/spindrift/commit/38d7d992a05df011dbb4cacb11d737519f9283d6))
* **research:** add agent-research labeled-event workflow ([44f884f](https://github.com/jordansmall/spindrift/commit/44f884f67ca763a79efc5cc9192fa6637fa37b41)), closes [#641](https://github.com/jordansmall/spindrift/issues/641)
* **workflows:** mint GitHub App tokens instead of PAT secrets ([be08ef4](https://github.com/jordansmall/spindrift/commit/be08ef4032b00690d2aa2ef717452d262690b0e6))
* **workflows:** pass knobs as --flags, not ambient env ([b4762e7](https://github.com/jordansmall/spindrift/commit/b4762e79e5ba37d7602eb75a471b3c4e5e63b35e))


### Miscellaneous Chores

* **dogfood:** forward knobs as flags, not env ([9eacdb7](https://github.com/jordansmall/spindrift/commit/9eacdb7faee013fe585d7ce4a9e89ac7059de4f3))
* **driver:** delete outcome/classify, heartbeat, claudetranscript ([226f5a8](https://github.com/jordansmall/spindrift/commit/226f5a884058396d9dd85745f35753da56c38071))


### Styles

* **forge:** gofmt Fake struct field alignment ([05b3550](https://github.com/jordansmall/spindrift/commit/05b3550c15f41b43ac98b1c8e0f2afc14b1d5bc6))
* **prompts:** nixfmt landing-token checks ([e19ebc9](https://github.com/jordansmall/spindrift/commit/e19ebc9f92532992775ec06e82cad48a5d7da6c0))
* **tests:** drop trailing blank line ([f2a6dbb](https://github.com/jordansmall/spindrift/commit/f2a6dbb64c04fe4b10f535190f68149100e842c3))
* **tests:** fix comment grammar ([2d34f42](https://github.com/jordansmall/spindrift/commit/2d34f42d134e925e7f84ef1ecbe2e937f8c581c4))
* **tests:** trim trailing blank line ([76acbcc](https://github.com/jordansmall/spindrift/commit/76acbccc1360849bfc108010c296b7acfeeb308c))

## [0.4.2](https://github.com/jordansmall/spindrift/compare/v0.4.1...v0.4.2) (2026-07-14)


### Features

* **dogfood:** enable the Filer on spindrift's own harness ([cfbdd64](https://github.com/jordansmall/spindrift/commit/cfbdd641414ff25f8131cd8354b8635ee67228e7)), closes [#616](https://github.com/jordansmall/spindrift/issues/616)


### Bug Fixes

* **nix:** scope the dogfood filer-model check to the filer entry ([667ed59](https://github.com/jordansmall/spindrift/commit/667ed5974c8653238aeef774e6863bf314fbc778))


### Documentation

* **claude:** document the agent-review-finding label ([f5413a1](https://github.com/jordansmall/spindrift/commit/f5413a188e34869bd335d586eea87acce6a4a391))

## [0.4.1](https://github.com/jordansmall/spindrift/compare/v0.4.0...v0.4.1) (2026-07-14)


### Features

* **checks:** scope in-box gate to source-level checks ([fe9f8d1](https://github.com/jordansmall/spindrift/commit/fe9f8d1d4e3dbdf3ff9311b36b3f722bcef10807))
* **dogfood:** abort when podman machine RAM undercuts MEMORY_LIMIT ([6c3dc1a](https://github.com/jordansmall/spindrift/commit/6c3dc1add92b30150a2e60a87c2c2d9aa85453d7)), closes [#580](https://github.com/jordansmall/spindrift/issues/580)
* **forge:** distinguish conflicts from blocked-by-checks refusals ([676049b](https://github.com/jordansmall/spindrift/commit/676049b41aa345f86a14eddc47136f61a7cfcde2))
* **mkharness:** install bash completion at build time ([9ba887e](https://github.com/jordansmall/spindrift/commit/9ba887e05bfe11ba3288604228f627772cfa7d01))
* **mkharness:** install fish completion at build time ([30aa69c](https://github.com/jordansmall/spindrift/commit/30aa69c6a63f2477d36c6bd6c684758448429fc9))
* **mkharness:** install zsh completion at build time ([4a2afb5](https://github.com/jordansmall/spindrift/commit/4a2afb573703a504f8c7ecf6a1ab1f50de508e79))
* **prompts:** forbid backgrounding blocking CHECK gates ([0bc8977](https://github.com/jordansmall/spindrift/commit/0bc8977047f58f473fdd757878593726f3093892))
* **renderers:** add renderBashCompletion for the CLI ([a411c42](https://github.com/jordansmall/spindrift/commit/a411c421662a416c3e9c7914dd42ae7d5e99be9e))
* **renderers:** add renderFishCompletion for the CLI ([6be1bf4](https://github.com/jordansmall/spindrift/commit/6be1bf474ed9cf57cbfad9f1e99e1e90f0323f29))
* **renderers:** add renderZshCompletion with per-flag descriptions ([de7a07a](https://github.com/jordansmall/spindrift/commit/de7a07a823c326027803505a1f87a806aa5d6b02))


### Bug Fixes

* **agent:** scope the backstop to a clean, outcome-less exit ([716452a](https://github.com/jordansmall/spindrift/commit/716452ac064ce8cb56ae6aa889e65858068b161e))
* **agent:** strip all lifecycle labels on claim ([02d2ced](https://github.com/jordansmall/spindrift/commit/02d2ced5ac10e7c712c97b6dd72f86b979082dd3))
* **agent:** synthesize a blocked outcome when the driver prints none ([f24cc9c](https://github.com/jordansmall/spindrift/commit/f24cc9c637b5d530e5c830ffbeb25b96edbdf923))
* **checks:** tighten bash-completion coverage guard ([ebbbda8](https://github.com/jordansmall/spindrift/commit/ebbbda8aa22b28f103ad413c5a34e083568ee50b))
* **dispatch:** don't re-dispatch a zero-exit hold when a PR exists ([d1c7e92](https://github.com/jordansmall/spindrift/commit/d1c7e929c2a4cc9c999d9dc13948d7f032edf4b7))
* **dispatch:** hold-and-retry zero-exit rate-limited boxes ([10e809f](https://github.com/jordansmall/spindrift/commit/10e809fa531582c732bc9c5d07ca357c7aec8a06)), closes [#565](https://github.com/jordansmall/spindrift/issues/565)
* **dispatch:** rotate stale per-issue log instead of truncating ([f482199](https://github.com/jordansmall/spindrift/commit/f482199ba1facf6bd434bd551c72b07ef13d3c50)), closes [#561](https://github.com/jordansmall/spindrift/issues/561)
* **dispatch:** skip already-in-flight box before touching its log ([865743e](https://github.com/jordansmall/spindrift/commit/865743e6c7ce74b526def1eeb38d0e486bb4e3ce))
* **dogfood:** guard jq failure in memory preflight ([82a060d](https://github.com/jordansmall/spindrift/commit/82a060dd25730e8fd96245a89e62228a2697460c))
* **dogfood:** stop baking skills with host pkgs ([8dc8cf0](https://github.com/jordansmall/spindrift/commit/8dc8cf0dcd32069745d3973f915a0cf1c4ae596e)), closes [#597](https://github.com/jordansmall/spindrift/issues/597)
* **dogfood:** treat empty MEMORY_LIMIT as disabling the preflight ([7763013](https://github.com/jordansmall/spindrift/commit/7763013b454a48326dc41005eaffd3f878b3b5f4))
* **image:** bake skill content with the image's own Linux pkgs ([463a8fc](https://github.com/jordansmall/spindrift/commit/463a8fcbe02cb000184cbc6f97539dda56fb2c20)), closes [#597](https://github.com/jordansmall/spindrift/issues/597)
* **launcher:** key freshness probe on image tag, not drvPath ([666a527](https://github.com/jordansmall/spindrift/commit/666a527549e07a1013430125f2fe9c95e57b8cec))
* **launcher:** print help on bare or unknown subcommand ([fcfc004](https://github.com/jordansmall/spindrift/commit/fcfc00478228258a4caaaa304e790dd93d66fa1e)), closes [#555](https://github.com/jordansmall/spindrift/issues/555)
* **launcher:** stop auto-adopting agent-in-progress issues ([c555800](https://github.com/jordansmall/spindrift/commit/c5558003c49b4bece8be9e62ae5f4f0f91392992)), closes [#600](https://github.com/jordansmall/spindrift/issues/600)
* **nix:** scope CHECK guardrail assertion to the CHECK block ([3c34502](https://github.com/jordansmall/spindrift/commit/3c345028a9067284cf3112c4b1f4a021b87e8997))
* **outcome:** scope rate-limit classification to real API errors ([ebe7276](https://github.com/jordansmall/spindrift/commit/ebe7276268668f8da969c2fadd25d60004d9f91c)), closes [#579](https://github.com/jordansmall/spindrift/issues/579)
* **renderers:** correct fish --no-build/--force descriptions ([1b1b7c4](https://github.com/jordansmall/spindrift/commit/1b1b7c4d832853145388bae133f8b397d8407491))
* **renderers:** fix dead post-subcommand flag completion in zsh ([4ab8b49](https://github.com/jordansmall/spindrift/commit/4ab8b49f771609bd193bdf51108ae862f8b32e09))
* **runner:** recognize already-running collision, don't launch ([2f6999e](https://github.com/jordansmall/spindrift/commit/2f6999ea93a794f389cdee27769d01148405791d))
* **settle:** re-wait for CI green after gate-driven force-push ([b3de6f4](https://github.com/jordansmall/spindrift/commit/b3de6f4ef8a5cd0b16b1d41c804106635ccbe408))
* **settle:** skip rebase-retry on blocked-by-checks refusals ([1a5553b](https://github.com/jordansmall/spindrift/commit/1a5553b4a9f9c712662165426c6adcc80d2d3081))
* **tests:** script Mergeable() query in the fake gh client ([cfc1ac4](https://github.com/jordansmall/spindrift/commit/cfc1ac4e0a9821b89066531b272ddfb43b7da734))
* **tests:** update bats reconcile fixtures for [#600](https://github.com/jordansmall/spindrift/issues/600) ([79dc3ff](https://github.com/jordansmall/spindrift/commit/79dc3ffc53a700fbb64cd85b5526c5e58d396a8b))
* **waves:** don't fail-transition an already-in-flight issue ([710aa2f](https://github.com/jordansmall/spindrift/commit/710aa2f844574ea597a3109ec2e560677f61eed4))
* **waves:** track in-run claims to stop stale-discovery dupes ([aa1b1a9](https://github.com/jordansmall/spindrift/commit/aa1b1a9345dc7dddcccb4123414dde2aeab52c0f)), closes [#560](https://github.com/jordansmall/spindrift/issues/560)


### Documentation

* address review nits from [#555](https://github.com/jordansmall/spindrift/issues/555) ([9f5b871](https://github.com/jordansmall/spindrift/commit/9f5b871d457db1dc21b67d4e42a885f650295f40))
* **adr:** describe the freshness probe's tag currency ([15a25ba](https://github.com/jordansmall/spindrift/commit/15a25ba41a8e9a2120b1ea14ed1119e933034937))
* correct bare-invocation help behavior ([7cd7970](https://github.com/jordansmall/spindrift/commit/7cd79702a8b6349d4d7220a4e0e4167246ebf2ad))
* **nix:** describe the checks-inbox vs full check split ([f777e53](https://github.com/jordansmall/spindrift/commit/f777e5342522662bc1711187bff7472af3c9437f))
* **nix:** explain the freshness-probe stakes on the drvPath guard ([56236e7](https://github.com/jordansmall/spindrift/commit/56236e7b135b1b1240e4223d7804ae6bce7c9df0)), closes [#598](https://github.com/jordansmall/spindrift/issues/598)
* **outcome:** correct Classify/scanLog doc comments ([393bf2b](https://github.com/jordansmall/spindrift/commit/393bf2b8e9115742991b5b5b3c9e97a8e92b1dcf))
* **readme:** document bash tab-completion ([9a87c5b](https://github.com/jordansmall/spindrift/commit/9a87c5b414fb95675c5a01f28da488043fbca781))
* **readme:** document fish tab-completion ([b0e9e7d](https://github.com/jordansmall/spindrift/commit/b0e9e7d8f4bb5d7f1822998a8bde2f417ad13728))
* **readme:** document zsh tab-completion ([0660874](https://github.com/jordansmall/spindrift/commit/06608742433b0a9d2434899b569df135cfc70e99))
* **readme:** point CONTRIBUTING row at checks-inbox ([e09fa77](https://github.com/jordansmall/spindrift/commit/e09fa7738ebe71060f471899ba11d1ececc72b1a))
* **readme:** state minimum podman machine memory for dogfood ([a1ffbd2](https://github.com/jordansmall/spindrift/commit/a1ffbd22da4d076835454a0758f5f7d19559cc3b))
* **reference:** document the skills content form ([e007c59](https://github.com/jordansmall/spindrift/commit/e007c59057f38facb5a0ccb24e4fd703c7787cf8)), closes [#597](https://github.com/jordansmall/spindrift/issues/597)
* **reference:** drop automatic stranded-issue reconcile claims ([7411629](https://github.com/jordansmall/spindrift/commit/7411629f3c91dae3f0097ead5086eed841db55cd))


### Code Refactoring

* **runner:** route hardcoded nix/bwrap calls through exec seam ([3add948](https://github.com/jordansmall/spindrift/commit/3add94838b4e8812629f59055f18d7138a051f79))


### Tests

* **checks:** guard bash completion coverage ([bf1066b](https://github.com/jordansmall/spindrift/commit/bf1066bfc6c018ef9632ba77dfc10c28d1436e55))
* **checks:** guard fish completion coverage ([ca65260](https://github.com/jordansmall/spindrift/commit/ca652605ef899909c77375589fd451ae175fd12f))
* **dispatch:** cover stale log preservation on rotate ([7bbfd99](https://github.com/jordansmall/spindrift/commit/7bbfd99545718086f47728dc121b090634d89a08))
* **entrypoint:** cover the outcome backstop's three paths ([99c11dc](https://github.com/jordansmall/spindrift/commit/99c11dc185fbcededca0270d5399afd4ebcb1f66))
* **entrypoint:** guard the backstop against a non-zero driver exit ([507a860](https://github.com/jordansmall/spindrift/commit/507a8601c157b7e0547cb6cb4e11f24d51a2a1b3))
* **fakes:** add FAKE_CLAUDE_NO_OUTCOME to the claude stub ([e54e312](https://github.com/jordansmall/spindrift/commit/e54e31262e4cc9dabc824e4d2a4883449ee24883))
* **fakes:** stub podman machine inspect in runtime fake ([e75ed29](https://github.com/jordansmall/spindrift/commit/e75ed29202f24535659f8941acc58226b53e5372))
* **freshness:** cover image-tag currency and livelock regression ([10acb6c](https://github.com/jordansmall/spindrift/commit/10acb6cac1fa6eea40e3108e8d320600eca6797a))
* **nix:** assert agent-image drvPath is host-independent ([2f62a9c](https://github.com/jordansmall/spindrift/commit/2f62a9c3a0a3116a92ad21edb628629bae2858ea)), closes [#597](https://github.com/jordansmall/spindrift/issues/597)
* **nix:** assert CHECK guardrail reaches both prompts ([c5b6563](https://github.com/jordansmall/spindrift/commit/c5b6563d2b788e19c24ed42adab1fdbda0c052ea))
* **runner:** assert bwrap enforces isolation, not just argv ([e3e32d9](https://github.com/jordansmall/spindrift/commit/e3e32d9acc92c6ba6ed0c798890d9e0522eeb8d1))
* **runner:** assert OCI hardening is enforced by the daemon ([5dd253e](https://github.com/jordansmall/spindrift/commit/5dd253e48f5a4414e30a9f2ed7c9bba283fec90a))
* **runner:** cover exec-seam orchestration paths ([33a749c](https://github.com/jordansmall/spindrift/commit/33a749cdf3ddaace75898632a46b3e250499e5a5))
* **runner:** cover isNoBuilderError predicate ([4ae8cc9](https://github.com/jordansmall/spindrift/commit/4ae8cc9e50f3fe24cedc31eb6dadf104a968a9c2))
* **runner:** fake-CLI harness + OCI orchestration coverage ([742bcd3](https://github.com/jordansmall/spindrift/commit/742bcd35d7f680fbcab00471013b9cc8b677262a)), closes [#574](https://github.com/jordansmall/spindrift/issues/574)
* **runner:** read container stdout apart from the daemon's pull progress ([e607cb5](https://github.com/jordansmall/spindrift/commit/e607cb5dadaad0553586898c7e58f303124d8cc3))
* **waves:** cover BuildEdges dependency-graph construction ([cae133e](https://github.com/jordansmall/spindrift/commit/cae133e6adcf5db78953226fa31db5da5c841389)), closes [#569](https://github.com/jordansmall/spindrift/issues/569)
* **waves:** cover nextReady cascade and overlap-defer ([f606b3e](https://github.com/jordansmall/spindrift/commit/f606b3eb4aa44dc94d149e488f5226fc1cb6e093)), closes [#570](https://github.com/jordansmall/spindrift/issues/570)
* **waves:** cover RunContinuous mid-refill cycle guard ([996a6b0](https://github.com/jordansmall/spindrift/commit/996a6b0178f081648e1551289c033941aa51ee2d)), closes [#571](https://github.com/jordansmall/spindrift/issues/571)
* **waves:** pin the suppressed-stale launcher output line ([bbc0db9](https://github.com/jordansmall/spindrift/commit/bbc0db9009b19f0e28a6424c3034b5b6e3096966))


### Build System

* **nix:** add go and bubblewrap to the dev shell ([82cb5ca](https://github.com/jordansmall/spindrift/commit/82cb5ca30e2e42ea1b5ab1a1667f155dc86cc84d))


### Continuous Integration

* run the runner sandbox integration tests on Linux ([4d7c820](https://github.com/jordansmall/spindrift/commit/4d7c820c79cc3eb9fe4fc69e0d6538a0e3c8f92f))

## [0.4.0](https://github.com/jordansmall/spindrift/compare/v0.3.0...v0.4.0) (2026-07-12)


### ⚠ BREAKING CHANGES

* **settings:** settings.concurrency.depsPollSecs/depsWaitSecs (DEPS_POLL_SECS/DEPS_WAIT_SECS) are removed. A consumer that still sets either gets an unknown-key eval error naming the valid keys, per the pre-1.0 policy (ADR 0010). MAX_JOBS still caps the wave size (0 = uncapped); re-invocation drains a dependency graph wave by wave.

### Features

* **dogfood:** drive slot-refill dispatch, handle exit 4 ([253c2fb](https://github.com/jordansmall/spindrift/commit/253c2fbb9370aee3929b5a364a01ae96904c9483)), closes [#528](https://github.com/jordansmall/spindrift/issues/528)
* **freshness:** add image-freshness probe seam ([ec575c6](https://github.com/jordansmall/spindrift/commit/ec575c6cdbcda9342d8a61a708cdf3149be49572))
* **launcher:** add internal/glob package (Match + Overlap) ([4b86264](https://github.com/jordansmall/spindrift/commit/4b8626424db085209e1eb2d145b72e0dbb616a54))
* **launcher:** add internal/waves package with Plan/Run ([5e5ecc3](https://github.com/jordansmall/spindrift/commit/5e5ecc3bde738eb8f64c57c6b67145034eab270a))
* **launcher:** wire CONTINUOUS_DISPATCH mode and exit code 4 ([8b529f0](https://github.com/jordansmall/spindrift/commit/8b529f0d490297a051ff7b25dfdfa2039a4cd1c7))
* **lib:** add prompt-inject module with eval-level tests ([ab95a3b](https://github.com/jordansmall/spindrift/commit/ab95a3ba41071dde2bce7d870faf255a261d7d72)), closes [#512](https://github.com/jordansmall/spindrift/issues/512)
* **nix:** add lib/preambles.nix renderers ([4875f44](https://github.com/jordansmall/spindrift/commit/4875f44979ac746d007604e377f0dbc2d80f4a89)), closes [#513](https://github.com/jordansmall/spindrift/issues/513)
* **preview:** surface image-freshness on the preview verb ([d08f749](https://github.com/jordansmall/spindrift/commit/d08f7493b0682c9ebe7ea514742fb957757b3803))
* **regen:** generate the bats box-env fixture and template settings ([8a86509](https://github.com/jordansmall/spindrift/commit/8a865096c5308c165e53d34bd963601afc908ffe))
* **runner:** add RunFunc hook to Fake for deterministic timing ([a011031](https://github.com/jordansmall/spindrift/commit/a011031032f83ea802e82e8abea4196f6c67d503))
* **settings:** drop DEPS_POLL_SECS/DEPS_WAIT_SECS from schema ([910636b](https://github.com/jordansmall/spindrift/commit/910636b074c62eb95187308587f5feb8eef35930))
* **waves:** add RunContinuous slot-refill dispatch engine ([07b3298](https://github.com/jordansmall/spindrift/commit/07b329894a84c894d1fdc09fbc2fe63481ae0cee))
* **waves:** print remaining issues and re-run command for selective drain ([38eb033](https://github.com/jordansmall/spindrift/commit/38eb0335c5ac0940ac4cd57681d9f298332fe4e0))
* **waves:** report remaining issues after a partial drain wave ([fbcd8a4](https://github.com/jordansmall/spindrift/commit/fbcd8a4fdea7016bc31db4b442bc392413a1fb2f))


### Bug Fixes

* **dispatch:** translate ErrOpenNoneDispatchable to exit 3 for selective ([8218be7](https://github.com/jordansmall/spindrift/commit/8218be7fec7e4861492b81d5951a3ccb6355642e))
* **entrypoint:** keep conflict-resolve out of the devShell ([e2abab6](https://github.com/jordansmall/spindrift/commit/e2abab6dc7ed298a1d465c8357deb1e98fc16781))
* **freshness:** surface git/nix stderr in fail-closed messages ([3772cdd](https://github.com/jordansmall/spindrift/commit/3772cdd059c502077ec57966490650359b0f0207))
* **launcher:** scope the touch-overlap precheck to run(), not selective ([c1df5fe](https://github.com/jordansmall/spindrift/commit/c1df5feb207be9f8bd1f14d05064e4607366d125))
* **settle:** guard MERGE_MODE=auto against a push-only forge ([bc0b60f](https://github.com/jordansmall/spindrift/commit/bc0b60fe08e0ff5cd553eb6ff2858aacd6d77d7e))
* **waves:** correct remaining-count wording for a MAX_JOBS cap ([d56c192](https://github.com/jordansmall/spindrift/commit/d56c1927b8bc4667d8a47846dc959ba39365486b))
* **waves:** route selective dispatch through ModeDrain ([f98227b](https://github.com/jordansmall/spindrift/commit/f98227b298db0df729bfcf518f6cc6d04338eb93))
* **waves:** unify queue dispatch on drain semantics ([bf30cfc](https://github.com/jordansmall/spindrift/commit/bf30cfc7c1fb3bf07edd7ee2fac5e93b194ba36a))


### Documentation

* **adr:** amend 0009 with copilot auth spike result ([c6bd7e5](https://github.com/jordansmall/spindrift/commit/c6bd7e5108d3c326469f1fcb255c81f1132e1559)), closes [#260](https://github.com/jordansmall/spindrift/issues/260)
* align MAX_JOBS wording and MIGRATING.md table ([1ae7879](https://github.com/jordansmall/spindrift/commit/1ae7879e20976fab7727f2a78c1bb59aac5f2749))
* drop stale dependency-wave poll/wait wording ([0297b95](https://github.com/jordansmall/spindrift/commit/0297b95694223a76d255ed8b67d6d8b30962577a))
* **nix:** repoint entrypoint.bats comments at the split suites ([4b56a7e](https://github.com/jordansmall/spindrift/commit/4b56a7eda163f86f59b17011b3871c071ddd472b))
* **readme:** document exit code 4 and CONTINUOUS_DISPATCH ([6b4b56e](https://github.com/jordansmall/spindrift/commit/6b4b56edd3357a9e937954b3f0af65a25a3a6230))
* **readme:** dogfood loop drives slot-refill dispatch ([af03d1f](https://github.com/jordansmall/spindrift/commit/af03d1f8fe0e56b9b466e501bc14fc42fc115afe))
* record the PRForge split in ADR 0013 and CONTEXT.md ([72eac62](https://github.com/jordansmall/spindrift/commit/72eac62c2518dfc3caee0d47f9ce0d09a87b5566))


### Code Refactoring

* **entrypoint:** fold Driver invocation into run_driver_in_env ([fed86fd](https://github.com/jordansmall/spindrift/commit/fed86fd3085c79ed4aa5d0efd196eb0017332109))
* **entrypoint:** restructure into phase functions behind main() ([ee3fec4](https://github.com/jordansmall/spindrift/commit/ee3fec437ecca1a8cc9163d614ce420b8c36f45b)), closes [#515](https://github.com/jordansmall/spindrift/issues/515)
* **entrypoint:** scope cross-phase sentinels local to main() ([48d69b3](https://github.com/jordansmall/spindrift/commit/48d69b3b4b092472887f1146171e23b623f2a233))
* **forge:** narrow CodeForge, add optional PRForge, retire Client ([96e8d86](https://github.com/jordansmall/spindrift/commit/96e8d86f88261977b571a48780777ed9e5ef7672))
* **image:** extract Box image assembly into lib/image.nix ([7a50ee4](https://github.com/jordansmall/spindrift/commit/7a50ee4c5bcdfae0031835fcb0dd32725a96d0a3)), closes [#514](https://github.com/jordansmall/spindrift/issues/514)
* **launcher:** bootstrap wires IssueTracker and CodeForge separately ([26de516](https://github.com/jordansmall/spindrift/commit/26de516371b94ab23a5a9d81f2b98581f41afa71))
* **launcher:** consume internal/waves from run and dispatch &lt;nums&gt; ([bb841bc](https://github.com/jordansmall/spindrift/commit/bb841bc82b14e87b02514c2bd566828bb3a5517e))
* **launcher:** delete dead deps-wave config wiring ([401819c](https://github.com/jordansmall/spindrift/commit/401819cbedcd2a9a1209ba542e697cf0bf636e7c))
* **launcher:** extract doctor subcommand into doctor.go ([85604f9](https://github.com/jordansmall/spindrift/commit/85604f95c53f339cd671e1dde1eef88f7271e3fa))
* **launcher:** extract preview formatting into preview.go ([cb2faae](https://github.com/jordansmall/spindrift/commit/cb2faae13bbb6f6a57c9adf28e087d609c5aed7e))
* **launcher:** Merge guard consumes internal/glob.Match ([9d5e89e](https://github.com/jordansmall/spindrift/commit/9d5e89e661e8bfca9c55889e1fa1b4917287e618))
* **launcher:** Touches overlap gate consumes internal/glob.Overlap ([6699986](https://github.com/jordansmall/spindrift/commit/669998607f0f390a3aa202e56e22453d1db6af01))
* **launcher:** wire mkHarness to lib/prompt-inject ([8285f43](https://github.com/jordansmall/spindrift/commit/8285f43497a93984bba15978622767873d0bcd94)), closes [#512](https://github.com/jordansmall/spindrift/issues/512)
* **mkHarness:** delegate preamble marshalling to lib/preambles.nix ([daeb810](https://github.com/jordansmall/spindrift/commit/daeb8105a24dc79e464ed75d947b061ed212d7a9)), closes [#513](https://github.com/jordansmall/spindrift/issues/513)
* **schema:** derive launcher-env-coverage exclusions ([3579f5b](https://github.com/jordansmall/spindrift/commit/3579f5bc3a5da78d674e1327cb29effc8e187035))
* **settle:** consume IssueTracker/CodeForge and PRForge assertion ([f63159c](https://github.com/jordansmall/spindrift/commit/f63159cf32e675466232f857c7d6e6a04cd4ea70))
* **waves:** delete batchHasTouchOverlap, dead since Run's reroute ([f7f56f2](https://github.com/jordansmall/spindrift/commit/f7f56f21ec5560bcf2efec01921b6e2f61614006))
* **waves:** delete the multi-wave dispatch loop ([6da7d4b](https://github.com/jordansmall/spindrift/commit/6da7d4b86bf7de596032dd5180b71edfd57c4081))
* **waves:** thread IssueTracker/CodeForge instead of forge.Client ([e32afe8](https://github.com/jordansmall/spindrift/commit/e32afe8f8a2c378f3f9cfa147dc69ad61c904c7c))


### Tests

* **bats:** rewrite dependency-wave integration tests for ADR 0019 ([1d975a6](https://github.com/jordansmall/spindrift/commit/1d975a6b04001b41e61a438d16adfefe000627b6))
* **entrypoint:** split entrypoint.bats into per-concern suites ([f2e1a3a](https://github.com/jordansmall/spindrift/commit/f2e1a3a8e5805ada743b00a132053ab1a6c36354))
* **helper:** extract setup_run_env for split run-*.bats suites ([e469e19](https://github.com/jordansmall/spindrift/commit/e469e19460ed53491b9da6f243cd3b8e0d3af730))
* **launcher:** pin a negative Match/Overlap consistency case ([3d3e7c3](https://github.com/jordansmall/spindrift/commit/3d3e7c360162be22f01aef69189d8c87a4482613))
* **run:** split run.bats into per-concern suites ([b617084](https://github.com/jordansmall/spindrift/commit/b617084431dd09615a32302e0c9e086ebd98c52f))
* **waves:** regression tests for [#477](https://github.com/jordansmall/spindrift/issues/477) exit-after-wave drain ([9d9f729](https://github.com/jordansmall/spindrift/commit/9d9f7296ab7cff7816edba3357b533d86ef128d8))


### Miscellaneous Chores

* bump flake ([ea78ca9](https://github.com/jordansmall/spindrift/commit/ea78ca92674e6994c7b3046a32f2a48d6ef8f8a1))
* gitignore claude local files ([1cab79e](https://github.com/jordansmall/spindrift/commit/1cab79e13e917dc53f2e731d3c141a4d29729b4b))

## [0.3.0](https://github.com/jordansmall/spindrift/compare/v0.2.1...v0.3.0) (2026-07-11)


### ⚠ BREAKING CHANGES

* spindrift no longer provides flake outputs for x86_64-darwin.

### Features

* **checks:** add driver-names-gen drift guard ([168f06c](https://github.com/jordansmall/spindrift/commit/168f06c1b7c6df51427102ceca5647174123dc48))
* **checks:** pin lifecycle-label literals in agent workflows ([719cd82](https://github.com/jordansmall/spindrift/commit/719cd8263b8615c823eb0cb71c6aa3dc8f8cb972))
* **claudetranscript:** add claude stream-json transcript-parse package ([0942e5a](https://github.com/jordansmall/spindrift/commit/0942e5a77b4517a0d901bfeac2cdabbfd64dafff))
* **dispatch:** add internal/dispatch, the per-issue execution module ([b869e5a](https://github.com/jordansmall/spindrift/commit/b869e5a40c0dd798d1aeea93b7f2400720a19a4b))
* **dogfood:** bake bats and shellcheck into spindrift box ([b504f5a](https://github.com/jordansmall/spindrift/commit/b504f5a4c2146a35138262b67deb46e165025dbd))
* **dogfood:** enable AUTO_LINT for spindrift's own runs ([543d2f8](https://github.com/jordansmall/spindrift/commit/543d2f8c8df6705fa2462afcfe9d4394664d2952))
* **dogfood:** enable in-box nix flake check for spindrift itself ([1140603](https://github.com/jordansmall/spindrift/commit/114060380e5b063fc3549ae29836d9603ccaa987))
* **dogfood:** parallel by default via bounded drain batches ([71db991](https://github.com/jordansmall/spindrift/commit/71db9919c821ad0584a7aff138797fc5e1707620)), closes [#476](https://github.com/jordansmall/spindrift/issues/476)
* **dogfood:** stop gracefully after the current wave ([03f62c3](https://github.com/jordansmall/spindrift/commit/03f62c3b8cdfc564e6f3d167e0a3e4b946af7956))
* **drain:** cascade-fail when in-batch blocker has failed ([96d46ef](https://github.com/jordansmall/spindrift/commit/96d46ef369b1aa488085d01777f9586822299859))
* **drain:** exit 3 when issues exist but none are dispatchable ([b9ffbbe](https://github.com/jordansmall/spindrift/commit/b9ffbbe5c97afb09b5812b110ea8f67fd6c9aac3))
* **driver:** generate nixDriverNames from Nix registry ([0deec83](https://github.com/jordansmall/spindrift/commit/0deec83663d9afc92924558922f7ceeb974aef01))
* **drivers:** claude declares its session-cache dir ([6b199b2](https://github.com/jordansmall/spindrift/commit/6b199b2e862b6870edca394d5b73ff4f6c29320c))
* **entrypoint:** extend outcome-contract injection to COMMS/CHECK ([bd274df](https://github.com/jordansmall/spindrift/commit/bd274df16444ba601a36a77134925cfa1e9a35bf))
* **entrypoint:** inject AUTO_FORMAT_STEP when knob is on ([b84c05b](https://github.com/jordansmall/spindrift/commit/b84c05bc81327ba059b856870a6e44a67ef677b0))
* **entrypoint:** inject AUTO_LINT_STEP when knob is on ([54b22cf](https://github.com/jordansmall/spindrift/commit/54b22cf324bee708f74d3fb24e05eceaf34012b0))
* **entrypoint:** warn loudly when the nix store is writable ([2d7e562](https://github.com/jordansmall/spindrift/commit/2d7e562b7dcc411ca4d913978012b0292b3eb32b))
* **forge:** add CodeForge.PushOnly() capability predicate ([d103d9f](https://github.com/jordansmall/spindrift/commit/d103d9f859581e47e1ba47470064b451fae70fc6))
* **harness:** add nixStoreWritable + extraClosures knobs ([6a67fcc](https://github.com/jordansmall/spindrift/commit/6a67fcc29140992616ce6fdb6b3c26321c24367b))
* **harness:** render Driver functions for bats preamble ([565dc47](https://github.com/jordansmall/spindrift/commit/565dc47223f5f71d233de713db5dbef619734b5e))
* **launcher:** add bootstrap for the shared dispatch prologue ([92cfb07](https://github.com/jordansmall/spindrift/commit/92cfb07df808c606bb27c5fe6ab39379b7fe69b7))
* **launcher:** read Driver mount targets from nix-baked env vars ([7a24412](https://github.com/jordansmall/spindrift/commit/7a2441258b9b9f3c6dbe04c563179ea954af8e76))
* **logscan:** add shared line-scan helper with oversized-line policy ([745f6f4](https://github.com/jordansmall/spindrift/commit/745f6f4568651c39aa5d17a559d9314bcf5ec5f2))
* **mkHarness:** bake prompt fragments into the image ([2468724](https://github.com/jordansmall/spindrift/commit/2468724bc7466d7c27edd565195d0bfd1b0ad3fd))
* **nix:** pin and bake upstream caveman skill into dogfood ([a463a20](https://github.com/jordansmall/spindrift/commit/a463a20cf993c7de3cdd781a74e7746b2a67b871))
* **oci:** print image-present line on EnsureReady early return ([5b5ce8b](https://github.com/jordansmall/spindrift/commit/5b5ce8b38000e4a8fba9e0f451d9e94ccb728f20))
* **prompts:** default in-box agents to caveman narration ([f790abc](https://github.com/jordansmall/spindrift/commit/f790abc57e734bf8a29e35349bf7cb85250291cf))
* **prompts:** fix prompt sources shared blocks instead of copying ([d4eb1a8](https://github.com/jordansmall/spindrift/commit/d4eb1a87234c6d1cc5295458b0e9a12ee39a8058))
* **runner:** compute mount decisions once via MountSpec ([c329bad](https://github.com/jordansmall/spindrift/commit/c329bad87d15b1eb1f1cb8326eaa02e74a0c7a80))
* **schema:** add AUTO_FORMAT boolean knob ([848c454](https://github.com/jordansmall/spindrift/commit/848c454ee9835453ef55fa5e01718335e88e6d58))
* **schema:** add AUTO_LINT boolean knob ([b9741ee](https://github.com/jordansmall/spindrift/commit/b9741eef1e05ccd920093305fc96017c8b422299))
* **settle:** add internal/settle, the merge-gate execution module ([5120911](https://github.com/jordansmall/spindrift/commit/5120911cb142104777a4ba08e0645d2eba890ab9))
* **tests:** add schema-derived set_box_env helper ([a96f310](https://github.com/jordansmall/spindrift/commit/a96f3100bda812f34a4baa4bda28e68e9d6f5f9e))


### Bug Fixes

* **checks:** add AUTO_LINT to boxEnvOnly list ([e4c2242](https://github.com/jordansmall/spindrift/commit/e4c224232ec8cc085b803c9065a85bf3afb9c7af))
* **checks:** drain tar stream in extraClosures greps (SIGPIPE race) ([d95b29a](https://github.com/jordansmall/spindrift/commit/d95b29a1c5ed9be36898def6053f8c21c33e69cf))
* **checks:** drift-guard caveman-default fragment into image ([4a25a5f](https://github.com/jordansmall/spindrift/commit/4a25a5f188bb9eaf8b24943a96f19a04620d8c01))
* **checks:** nixfmt the new per-concern check modules ([6cb403e](https://github.com/jordansmall/spindrift/commit/6cb403e9273bb7bb89e0df9b30d957de9c47bdca))
* **checks:** tighten caveman drift guard to non-empty file ([adcce72](https://github.com/jordansmall/spindrift/commit/adcce7206989d069afaffd8b7fc58b14ad8530b6))
* **checks:** use a distinctive literal for the WATCH CI grep pin ([42e6e89](https://github.com/jordansmall/spindrift/commit/42e6e89cd59c173bbbba961035ab98080a950c53))
* **checks:** use a plain string for the groupOrder entry regex ([121b072](https://github.com/jordansmall/spindrift/commit/121b072532b5379483badaea3894de7e5c6e7db7))
* **dogfood:** cover claudetranscript in heartbeatFilterBin fileset ([b2bd6b3](https://github.com/jordansmall/spindrift/commit/b2bd6b332239716685c299df804ec1a0f1aa6661))
* **dogfood:** migrate off deprecated nix app aliases ([57dd9fe](https://github.com/jordansmall/spindrift/commit/57dd9fe7e821fbc044a705616798eb53c1962a7f)), closes [#431](https://github.com/jordansmall/spindrift/issues/431)
* **dogfood:** write the pid file after the dirty-tree check ([6c4dc50](https://github.com/jordansmall/spindrift/commit/6c4dc50013a711f20ed8ad4501ffcaa55f272091))
* **drain:** guard cascade behind issueNumber=="" check ([7a73369](https://github.com/jordansmall/spindrift/commit/7a73369cc3e99e4ff2c6e96da27540fe5ba96040))
* **entrypoint:** restore blank-line separator after fragment reads ([a244d39](https://github.com/jordansmall/spindrift/commit/a244d3941add89b31cf27434a50c8336eff48eb7))
* **entrypoint:** suppress SC2016 on AUTO_FORMAT_STEP ([94029d3](https://github.com/jordansmall/spindrift/commit/94029d37cb604cb5bb86dd61bd88acafcaa7a9ea))
* **fixtures:** sync harness defaults with dogfood module ([b1a1f53](https://github.com/jordansmall/spindrift/commit/b1a1f534c94fdaec38c2b3e46bd4eae53dfa08f4))
* **fixtures:** sync harness defaults with dogfood module ([faf8d2d](https://github.com/jordansmall/spindrift/commit/faf8d2ded2d9fe2134d21fc7171157cb18c3f5ab))
* **image:** bake /home/agent/.claude/projects owned 1000:1000 ([9cc5788](https://github.com/jordansmall/spindrift/commit/9cc578818ff6d4227a888d19faf2990faf6c6c51)), closes [#447](https://github.com/jordansmall/spindrift/issues/447)
* **launcher:** delete printOutcomeReport ([6790d81](https://github.com/jordansmall/spindrift/commit/6790d812f6dc87a6902758e894469ddaa6aca40e))
* **mkHarness:** collapse flagDflt to single line ([739c2e8](https://github.com/jordansmall/spindrift/commit/739c2e8ee69a09f0c8c616e635cf30ee4f0bfcad))
* **mkHarness:** restrict heartbeatFilterBin src via fileset ([e28ab3f](https://github.com/jordansmall/spindrift/commit/e28ab3f1b2cb3d0f8965b257ae9c1414f65e329b))
* **prompts:** move CAVEMAN_STEP off the COMMS slice boundary ([50e3020](https://github.com/jordansmall/spindrift/commit/50e3020dbb6e5572437c6944726e1b5e8288b7c5))
* **prompts:** steer AUTO-FORMAT away from doomed nix fmt ([364fe98](https://github.com/jordansmall/spindrift/commit/364fe98cc974b4dcc4687ea399ccccfdb01e3452))
* **runner/bwrap:** create agent-owned .claude parent before bind ([b7e031c](https://github.com/jordansmall/spindrift/commit/b7e031c7d30d168eeda18d74c2965a11f6971bc6)), closes [#447](https://github.com/jordansmall/spindrift/issues/447)
* **runner:** bwrap skills fallback must not mask a broken override ([161da81](https://github.com/jordansmall/spindrift/commit/161da81903a51a88da99bec49eaa19e072c0de8f))
* **template:** add promptSkillIteration to settings example ([eec74aa](https://github.com/jordansmall/spindrift/commit/eec74aaadaff8a3bd9ccc5adabb508a0e9667f40))
* **test:** check projects mountpoint in Layers[-1] only ([98b9ac7](https://github.com/jordansmall/spindrift/commit/98b9ac70c9c90867e4f503434ddf8f98959b5fa0))


### Documentation

* **adr:** dispatch exits at the wave boundary ([88bcfcf](https://github.com/jordansmall/spindrift/commit/88bcfcfb19dcd074d37ccf6407136c4eaa9f6cad))
* **adr:** lifecycle stays call-site transitions ([392c93a](https://github.com/jordansmall/spindrift/commit/392c93acd4662e98b21f29715be0e57b7cf84ed9)), closes [#139](https://github.com/jordansmall/spindrift/issues/139)
* **context:** add dispatch and settle terms ([4a24988](https://github.com/jordansmall/spindrift/commit/4a249887aa5b2dd88a68bcccafed2300e4383f52))
* document AUTO_FORMAT knob in README and CHANGELOG ([279ed39](https://github.com/jordansmall/spindrift/commit/279ed3993ec695dee09a18d67e5c6bdf7524b53a))
* document AUTO_LINT knob in README and CHANGELOG ([b557060](https://github.com/jordansmall/spindrift/commit/b5570600c35627b6484b81e9ec1357d6ad6db719))
* document caveman-default narration and opt-out ([cdddeb4](https://github.com/jordansmall/spindrift/commit/cdddeb4d3005b9b596049399a8fbc79753f37c8c))
* document the baked caveman skill ([953dde7](https://github.com/jordansmall/spindrift/commit/953dde7d373f7d1fc647b2490ee2da65f238f7ce))
* drop nix fmt from AUTO_FORMAT docs ([1984890](https://github.com/jordansmall/spindrift/commit/1984890f571592af1194f4a9a101c143c5c8f295))
* fix stale pass-1 comment, clarify Message field union ([a6242ed](https://github.com/jordansmall/spindrift/commit/a6242edea8662146006dc16c9673f516bdf8a450))
* **forge:** fix stale issueQueryLimit reference in jira test ([0375d3e](https://github.com/jordansmall/spindrift/commit/0375d3e61f5245bfb72602037fbebb7444350aec))
* instruct agents to shellcheck shell files in-box ([493f6a5](https://github.com/jordansmall/spindrift/commit/493f6a550987077d57d87c4f696e21919727d5a5))
* **nix:** credit ADR 0018 knobs to issue [#469](https://github.com/jordansmall/spindrift/issues/469), not [#470](https://github.com/jordansmall/spindrift/issues/470) ([9b8c5f1](https://github.com/jordansmall/spindrift/commit/9b8c5f1e8604e1f3f74094bbe2c83cc69d5bdaae))
* **nix:** make nix flake check the primary in-box gate ([2bf08e5](https://github.com/jordansmall/spindrift/commit/2bf08e5a6d65e1040d2fdd0efdeba20858e79811))
* note fix-pass transient retry now holds instead of burning ([a790d65](https://github.com/jordansmall/spindrift/commit/a790d659cffbefb740efbc8937af7809c9dfd08f))
* record ADR 0018 and the new option surface ([6ec6273](https://github.com/jordansmall/spindrift/commit/6ec6273a663b2df7e9dbf131090a9e868f18107b))
* **reference:** document Driver-authoring mount-target fields ([3d21008](https://github.com/jordansmall/spindrift/commit/3d2100809a4ea18ce05ae76801af23f80b53f774))
* **reference:** document fragment/SPINDRIFT_PROMPT_DIR interaction ([b6fcf27](https://github.com/jordansmall/spindrift/commit/b6fcf27acad57546ebe61fd09d0ecac1518eac8c))
* **reference:** note image bakes the driver-cache mountpoint ([67b3346](https://github.com/jordansmall/spindrift/commit/67b3346e0d6517ae3fda5682b488370ef9aeb02c))
* **settle:** note Settle's concurrent-use safety contract ([7e0933c](https://github.com/jordansmall/spindrift/commit/7e0933ca63bc89389c404db43fa5e2d0e77af401))
* **tests:** fix stale entrypoint.bats comments post-set_box_env ([8d549e1](https://github.com/jordansmall/spindrift/commit/8d549e11f5686a30b229ad9676ef8a8e82352865))


### Code Refactoring

* **checks:** manpage check renders toKebab via renderers.nix ([9f31e93](https://github.com/jordansmall/spindrift/commit/9f31e933a938fcf296c10a3f5d0100a86a55c510))
* **checks:** split nix/checks.nix into per-concern modules ([0edd67d](https://github.com/jordansmall/spindrift/commit/0edd67d39758a18cff017605065ea74b7135eba9))
* **dispatch:** skip cache creation when Driver declares none ([31af707](https://github.com/jordansmall/spindrift/commit/31af707a36be7dc94efdf18286d81848a8e6ff40))
* **entrypoint:** delete fallback Driver function bodies ([bf664e0](https://github.com/jordansmall/spindrift/commit/bf664e0125690c034f605e96c9b23075154669b4))
* **entrypoint:** move conditional prompt prose to fragment files ([eb8a373](https://github.com/jordansmall/spindrift/commit/eb8a37347a1d7bdd8abe18828f08348dc1dad14a))
* **entrypoint:** render the driver pipeline from one source ([c200083](https://github.com/jordansmall/spindrift/commit/c200083902e954b9ea87e66d5ec708867a86c1eb))
* **fixtures:** share dogfood leaf values between wiring paths ([3f3b4db](https://github.com/jordansmall/spindrift/commit/3f3b4dba5547890ab0d27f6c4d48e076a0d12282))
* **flakeModule:** import groupToAttr from renderers.nix ([cdbd924](https://github.com/jordansmall/spindrift/commit/cdbd9245e489e3b6786b6947609f8e47059c12f5))
* **forge:** dedup "## Comments" append helper ([5939663](https://github.com/jordansmall/spindrift/commit/5939663adfa5a4570ebf7b3251d35644cf75d671))
* **forge:** dedup page-limit cap and backlog warning ([c79bff9](https://github.com/jordansmall/spindrift/commit/c79bff9273ae0f94f8938c4fb2aa24dd23a6c341))
* **forge:** delete DefaultDispatchLabels, fake takes labels ([1ce6293](https://github.com/jordansmall/spindrift/commit/1ce6293701c2c672ec91ab73dcb956ffb92669a7))
* **forge:** split exec.go by responsibility ([b06ca89](https://github.com/jordansmall/spindrift/commit/b06ca89deb503078813f3df3ef931ae808a97313))
* **forge:** typed states, AgentBranch owner ([72c05ae](https://github.com/jordansmall/spindrift/commit/72c05aeff171555bb113ee19feb453c85352237e))
* **heartbeat:** consume claudetranscript; split formatting helpers ([a7a526d](https://github.com/jordansmall/spindrift/commit/a7a526d55a93756bf208596c7f02a5e613bdc262))
* **launcher:** delete newRunner/newBuildRunner wrappers ([999473e](https://github.com/jordansmall/spindrift/commit/999473e262e11e5bb3156f77435b3c84915c609f))
* **launcher:** drive dispatch through a per-issue Factory ([b348a69](https://github.com/jordansmall/spindrift/commit/b348a69f3121adb368d02342932e2b498c0710c9))
* **launcher:** drive the merge gate through settle.Settle ([c6ade73](https://github.com/jordansmall/spindrift/commit/c6ade73876c2b6663f0f7bec0793b3c4685a5be3))
* **launcher:** recover and dispatch &lt;nums&gt; use bootstrap ([24f848e](https://github.com/jordansmall/spindrift/commit/24f848ee0142e1dce1b8583df63990a330ab81c4))
* **launcher:** run takes a launchContext, wired via bootstrap ([c8f6eef](https://github.com/jordansmall/spindrift/commit/c8f6eef955a799cc21d8b2ea46408273d6176d89))
* **launcher:** thread launchContext through subcommand functions ([3e6569d](https://github.com/jordansmall/spindrift/commit/3e6569d525b64cbf3738a9038af6119fc3133b44))
* **logscan:** drop unused test helper params, cross-ref caveat ([ef2a43d](https://github.com/jordansmall/spindrift/commit/ef2a43d49773a8cf62efc1d4118bc4fe77ce7340))
* **mkHarness:** extract shared driverFunctionDefs binding ([cd523ee](https://github.com/jordansmall/spindrift/commit/cd523eee4d8a00984fdc9e52152617746efa08a9))
* **mkHarness:** render the man page via renderers.nix ([799b2f9](https://github.com/jordansmall/spindrift/commit/799b2f9b73f4dd79dfd71e7d887032f04d128427))
* **outcome,usage:** migrate scans onto logscan.ForEachLine ([4ec8e2a](https://github.com/jordansmall/spindrift/commit/4ec8e2aa1891af59c6a5c32da91a963b6dee0eca))
* **outcome:** migrate classify scan onto logscan.ForEachLine ([c396f38](https://github.com/jordansmall/spindrift/commit/c396f38ead9d6e1ae9cc04e18eb4ff02cda24bf8))
* **renderers:** promote helpers, add man-page renderer ([14c5da0](https://github.com/jordansmall/spindrift/commit/14c5da0ed677aa48dc4062acfb750095af25a113))
* **runner:** adapters take Driver-declared mount targets ([de12207](https://github.com/jordansmall/spindrift/commit/de12207b1a9a6e1c9425d4f1f8b041f32f823561))
* **runner:** bwrap adapter renders MountSpec, not its own gates ([1f8b4ff](https://github.com/jordansmall/spindrift/commit/1f8b4ffbb8d4f8ad87db934d5b9a8fa45820bbd9))
* **runner:** move RUNTIME validation next to Config ([11e7080](https://github.com/jordansmall/spindrift/commit/11e7080f90cb4d5a7cad88db3abc0ef97e32af24))
* **runner:** NewOCI/NewBwrap/NewBwrapBuild take Config ([22b7359](https://github.com/jordansmall/spindrift/commit/22b7359ac8b327bd5b2b23a6f0603adb59bfd8fc))
* **runner:** OCI adapter renders MountSpec, not its own gates ([6806eb6](https://github.com/jordansmall/spindrift/commit/6806eb67f354f842223bbf002220bfb9fe61921e))
* **runner:** thread Driver mount targets through Config ([d3ea5d7](https://github.com/jordansmall/spindrift/commit/d3ea5d79446fca7e14e0e64ad2024c0b9cbed279))
* **settle,forge:** per-group validation next to Config ([f935f30](https://github.com/jordansmall/spindrift/commit/f935f3073ab0059f513796f75b5637e639845fef))
* **tests:** entrypoint.bats consumes set_box_env ([dd496ed](https://github.com/jordansmall/spindrift/commit/dd496ed01f8764fdb7045c8eddf3b7406bfffda3))
* unify fan-out terminology on wave ([c528e22](https://github.com/jordansmall/spindrift/commit/c528e22b63b45c9e79347dc49a546380bc72bfbb))
* **usage:** consume claudetranscript for role resolution ([03ea1f8](https://github.com/jordansmall/spindrift/commit/03ea1f81b9d5a87df8fbc0883b5b6134c00cdd18))


### Tests

* **checks:** assert bats/shellcheck baked into dogfood ([57eb06f](https://github.com/jordansmall/spindrift/commit/57eb06f634c47f324cac45aa3ecd89c054b0c35f))
* **checks:** assert both sides of the new nix image knobs ([be7e89d](https://github.com/jordansmall/spindrift/commit/be7e89d1f0d61ad1d85118a7fcaa5757b6ce7352))
* **checks:** assert heartbeat-filter src excludes tests ([32ade8f](https://github.com/jordansmall/spindrift/commit/32ade8f8a7c588351373af87ab7a163e0e499c97))
* **checks:** drift-guard the caveman skill baked into dogfood ([9a55019](https://github.com/jordansmall/spindrift/commit/9a55019a37867fc7dcd4b375ded091c008c5df4d))
* **checks:** guard set_box_env against schema drift ([ea9595a](https://github.com/jordansmall/spindrift/commit/ea9595a5c3d9b01b5859ae4d041eab2feefd8ac5))
* **checks:** pin dogfood leaf values to one definition ([27119d5](https://github.com/jordansmall/spindrift/commit/27119d5dad186f98cd4d3188b94297ed40e918d1))
* **checks:** pin flags.go groupOrder against renderers.nix ([010fa4a](https://github.com/jordansmall/spindrift/commit/010fa4a56dec13dddd98c51012a9449c4ce63b40))
* **classify:** cover all transient markers and Driver seam ([a433176](https://github.com/jordansmall/spindrift/commit/a43317680eaa3a9f649ce87b651406d0ac28ea2f))
* **classify:** isolate bare Overloaded and no-such-host markers ([c075fb6](https://github.com/jordansmall/spindrift/commit/c075fb635a8eebd551cf35c6623194644cdb5011))
* **entrypoint:** cover /caveman skill preamble advertisement ([bf79c05](https://github.com/jordansmall/spindrift/commit/bf79c05704a5bec23291bd3370fb5e7fbd15bff4))
* **entrypoint:** cover AUTO-FORMAT step gating ([c9d4fdd](https://github.com/jordansmall/spindrift/commit/c9d4fddfa82bc747fe21157c214f3d4e0e7f8661))
* **entrypoint:** cover AUTO-LINT step gating ([9d01ce9](https://github.com/jordansmall/spindrift/commit/9d01ce93567d9cbd1ca39f9cba284e4aa0642f11))
* **entrypoint:** cover caveman-default narration default ([492a3aa](https://github.com/jordansmall/spindrift/commit/492a3aa1f73865b8b3bd60dee38a709c689c2d2e))
* **launcher:** migrate tests onto dispatch.Factory/Fake, add pin ([109f3a9](https://github.com/jordansmall/spindrift/commit/109f3a93b800050298acb44c91bf56c3f162d428))
* **launcher:** pin no state literal outside forge ([00c22a4](https://github.com/jordansmall/spindrift/commit/00c22a43c8cf55f450cf8b67d8a40a17f17d61fb))
* **runner:** pin no-duplication and cross-backend mount rendering ([a2b49f0](https://github.com/jordansmall/spindrift/commit/a2b49f09ff5d7900515ce17681905372585891ce))
* **settle:** pass real labels to git-forge merged-status test ([aaa6d81](https://github.com/jordansmall/spindrift/commit/aaa6d81ae88ff7dafd2e9912dc8f97203031d372))


### Build System

* drop x86_64-darwin from supported systems ([ee448fb](https://github.com/jordansmall/spindrift/commit/ee448fb8046d4e5f53de91e368bb5de6b4dcb6ac))


### Continuous Integration

* **actions:** add agent-setup composite action ([e87ab1c](https://github.com/jordansmall/spindrift/commit/e87ab1ce6a9695d0e64eb2a635355544665a8eb5))
* **workflows:** wire dispatch/recover to agent-setup ([b647627](https://github.com/jordansmall/spindrift/commit/b6476277f2676675ac31099603363ae49fa37f50)), closes [#458](https://github.com/jordansmall/spindrift/issues/458)


### Miscellaneous Chores

* **regen:** regenerate schema-derived artifacts for AUTO_FORMAT ([cc3e61c](https://github.com/jordansmall/spindrift/commit/cc3e61cb898a6b8b7f65c897f2d9512b6d348c86))
* **regen:** update generated surfaces for AUTO_LINT ([263970e](https://github.com/jordansmall/spindrift/commit/263970e7b74ff58351876783bddb738adafc957c))


### Styles

* **checks:** nixfmt equivalence.nix ([f47fa28](https://github.com/jordansmall/spindrift/commit/f47fa28b6c970e9c7bdd88b02d8f1860cc6a9c94))
* **checks:** nixfmt image.nix ([8a21ae6](https://github.com/jordansmall/spindrift/commit/8a21ae63c474753b62dcad9968e91d97f5dac711))
* **checks:** nixfmt the caveman drift guard ([57e34aa](https://github.com/jordansmall/spindrift/commit/57e34aa882feff633d9e02da2e095c76fd95c714))
* **entrypoint:** restore blank line dropped by fragment refactor ([c0131a6](https://github.com/jordansmall/spindrift/commit/c0131a66dd5e10daa8a42c107441305af5ef7afe))
* nixfmt lib/mkHarness.nix and nix/checks/prompts.nix ([c77701d](https://github.com/jordansmall/spindrift/commit/c77701df6cc404e7321c04ecf50ea0cc70fac72c))
* **nix:** nixfmt the multi-item inherit lists ([a1f5d47](https://github.com/jordansmall/spindrift/commit/a1f5d477c77a56f3bfa1b3e66493cb518b45573e))

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
